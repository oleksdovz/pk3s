// Package engine executes a plan step by step, persisting progress after
// every step so an interrupted deployment can be resumed.
package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/oleksdovz/pk3s/internal/k3s"
	"github.com/oleksdovz/pk3s/internal/plan"
	"github.com/oleksdovz/pk3s/internal/pve"
	"github.com/oleksdovz/pk3s/internal/runner"
	"github.com/oleksdovz/pk3s/internal/state"
)

// EventKind classifies progress events.
type EventKind int

// Event kinds.
const (
	EventStepStart EventKind = iota
	EventStepDone
	EventStepSkipped
	EventStepFailed
	EventLog
	EventFinished
)

// Event is emitted to the UI. Messages are already redacted.
type Event struct {
	Kind    EventKind
	StepID  string
	Title   string
	Index   int
	Total   int
	Message string
}

// ErrStopped is returned when the user requested a stop between steps.
var ErrStopped = errors.New("stopped by user; progress saved")

// FS abstracts host filesystem writes for testability.
type FS interface {
	MkdirAll(path string, perm os.FileMode) error
	WriteFile(path string, data []byte, perm os.FileMode) error
	Remove(path string) error
}

// OSFS is the real filesystem.
type OSFS struct{}

// MkdirAll implements FS.
func (OSFS) MkdirAll(p string, perm os.FileMode) error { return os.MkdirAll(p, perm) }

// WriteFile implements FS.
func (OSFS) WriteFile(p string, d []byte, perm os.FileMode) error {
	if err := os.WriteFile(p, d, perm); err != nil {
		return err
	}
	return os.Chmod(p, perm)
}

// Remove implements FS.
func (OSFS) Remove(p string) error { return os.Remove(p) }

// ArtifactSource provides a verified k3s binary.
type ArtifactSource interface {
	EnsureBinary(ctx context.Context, cacheDir, version, arch string) (k3s.Artifact, error)
}

// Timeouts for waiting operations.
type Timeouts struct {
	CTReady      time.Duration
	ServerReady  time.Duration
	AgentReady   time.Duration
	ClusterReady time.Duration
	Poll         time.Duration
}

// DefaultTimeouts are generous for slow homelab disks.
var DefaultTimeouts = Timeouts{
	CTReady:      2 * time.Minute,
	ServerReady:  6 * time.Minute,
	AgentReady:   4 * time.Minute,
	ClusterReady: 10 * time.Minute,
	Poll:         3 * time.Second,
}

// Engine runs a plan.
type Engine struct {
	Plan     *plan.Plan
	Store    *state.Store
	State    *state.State
	Secrets  *state.Secrets
	R        runner.Runner
	PCT      pve.PCT
	Pveam    pve.Pveam
	Fetch    ArtifactSource
	FS       FS
	Redactor *runner.Redactor
	Notify   func(Event)
	Sleep    func(time.Duration)
	Timeouts Timeouts

	artifact *k3s.Artifact
	stop     atomic.Bool
}

// RequestStop asks the engine to stop after the current step.
func (e *Engine) RequestStop() { e.stop.Store(true) }

func (e *Engine) emit(ev Event) {
	if e.Notify == nil {
		return
	}
	ev.Message = e.redact(ev.Message)
	e.Notify(ev)
}

func (e *Engine) logf(format string, args ...any) {
	e.emit(Event{Kind: EventLog, Message: fmt.Sprintf(format, args...)})
}

func (e *Engine) redact(s string) string {
	if e.Redactor == nil {
		return s
	}
	return e.Redactor.Redact(s)
}

func (e *Engine) sleep(d time.Duration) {
	if e.Sleep != nil {
		e.Sleep(d)
		return
	}
	time.Sleep(d)
}

// Run executes all pending steps. Completed steps are skipped, so Run can
// be called again after a failure or interruption.
func (e *Engine) Run(ctx context.Context) error {
	if e.Timeouts == (Timeouts{}) {
		e.Timeouts = DefaultTimeouts
	}
	if e.Redactor != nil && e.Secrets != nil {
		for _, s := range []string{e.Secrets.ServerToken, e.Secrets.AgentToken, e.Secrets.SecureServerToken, e.Secrets.SecureAgentToken} {
			e.Redactor.Add(s)
		}
	}
	steps := e.Plan.Steps
	for i, st := range steps {
		ev := Event{StepID: st.ID, Title: st.Title, Index: i, Total: len(steps)}
		if e.State.Steps[st.ID].Status == state.StatusDone && st.Kind != plan.StepArtifact {
			ev.Kind = EventStepSkipped
			e.emit(ev)
			continue
		}
		if e.stop.Load() {
			return ErrStopped
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		ev.Kind = EventStepStart
		e.emit(ev)
		e.State.Steps[st.ID] = state.StepState{Status: state.StatusRunning, Started: time.Now().UTC()}
		if err := e.Store.Save(e.State); err != nil {
			return fmt.Errorf("save state: %w", err)
		}

		err := e.runStep(ctx, st)

		ss := e.State.Steps[st.ID]
		ss.Finished = time.Now().UTC()
		if err != nil {
			ss.Status = state.StatusFailed
			ss.Error = e.redact(err.Error())
			e.State.Steps[st.ID] = ss
			_ = e.Store.Save(e.State)
			ev.Kind = EventStepFailed
			ev.Message = err.Error()
			e.emit(ev)
			return &StepError{Step: st, Err: errors.New(e.redact(err.Error()))}
		}
		ss.Status = state.StatusDone
		ss.Error = ""
		e.State.Steps[st.ID] = ss
		if err := e.Store.Save(e.State); err != nil {
			return fmt.Errorf("save state: %w", err)
		}
		ev.Kind = EventStepDone
		e.emit(ev)
	}
	e.emit(Event{Kind: EventFinished, Total: len(steps), Index: len(steps)})
	return nil
}

// StepError wraps a failed step.
type StepError struct {
	Step plan.Step
	Err  error
}

func (s *StepError) Error() string { return fmt.Sprintf("step %q failed: %v", s.Step.Title, s.Err) }
func (s *StepError) Unwrap() error { return s.Err }

func (e *Engine) runStep(ctx context.Context, st plan.Step) error {
	var node plan.Node
	if st.Node >= 0 {
		node = e.Plan.Nodes[st.Node]
	}
	switch st.Kind {
	case plan.StepHostModules:
		return e.hostModules(ctx)
	case plan.StepTemplate:
		return e.template(ctx)
	case plan.StepArtifact:
		_, err := e.ensureArtifact(ctx)
		return err
	case plan.StepCreateCT:
		return e.createCT(ctx, node)
	case plan.StepConfigureCT:
		return e.configureCT(ctx, node)
	case plan.StepStartCT:
		return e.startCT(ctx, node)
	case plan.StepPrepareNode:
		return e.prepareNode(ctx, node)
	case plan.StepInstallK3s:
		return e.installK3s(ctx, node)
	case plan.StepWaitNode:
		return e.waitNode(ctx, node)
	case plan.StepFetchTokens:
		return e.fetchTokens(ctx)
	case plan.StepClusterReady:
		return e.clusterReady(ctx)
	case plan.StepKubeconfig:
		return e.kubeconfig(ctx)
	default:
		return fmt.Errorf("unknown step kind %q", st.Kind)
	}
}

func (e *Engine) bootstrap() plan.Node { return e.Plan.Nodes[0] }

func (e *Engine) addResource(kind, id string) error {
	e.State.AddResource(state.Resource{Kind: kind, ID: id, Node: e.Plan.Spec.Node})
	return e.Store.Save(e.State)
}

// tmpFile writes data to a 0600 file in the cluster tmp dir and returns its
// path and a cleanup func.
func (e *Engine) tmpFile(name string, data []byte) (string, func(), error) {
	p := e.Store.Path("tmp/" + name)
	if err := e.FS.WriteFile(p, data, 0o600); err != nil {
		return "", func() {}, err
	}
	return p, func() { _ = e.FS.Remove(p) }, nil
}

func (e *Engine) ensureArtifact(ctx context.Context) (k3s.Artifact, error) {
	if e.artifact != nil {
		return *e.artifact, nil
	}
	a, err := e.Fetch.EnsureBinary(ctx, e.Store.CacheDir(), e.Plan.K3sVersion, e.Plan.Arch)
	if err != nil {
		return k3s.Artifact{}, err
	}
	e.logf("k3s %s (%s) verified, sha256 %s", a.Version, a.Arch, a.SHA256)
	e.artifact = &a
	return a, nil
}
