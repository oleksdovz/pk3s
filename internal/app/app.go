// Package app wires the building blocks together for the CLI and the TUI.
package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/oleksdovz/pk3s/internal/engine"
	"github.com/oleksdovz/pk3s/internal/k3s"
	"github.com/oleksdovz/pk3s/internal/logx"
	"github.com/oleksdovz/pk3s/internal/plan"
	"github.com/oleksdovz/pk3s/internal/pve"
	"github.com/oleksdovz/pk3s/internal/runner"
	"github.com/oleksdovz/pk3s/internal/spec"
	"github.com/oleksdovz/pk3s/internal/state"
	"github.com/oleksdovz/pk3s/internal/validate"
)

// App holds shared dependencies.
type App struct {
	Root     string
	R        runner.Runner
	Fetch    k3s.Fetcher
	Redactor *runner.Redactor
	// Probe live network/filesystem state during validation.
	LiveProbes bool
	Version    string
	// Log is the activity log (nil disables logging).
	Log *logx.Logger
}

// DefaultLogDir holds the activity log.
const DefaultLogDir = "/var/log/pk3s"

// Logf writes to the activity log.
func (a *App) Logf(format string, args ...any) { a.Log.Printf(format, args...) }

// OpenLog opens <dir>/pk3s.log (dir 0700, file 0600). Every executed
// command is logged from then on.
func (a *App) OpenLog(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(dir, 0o700)
	l, err := logx.Open(filepath.Join(dir, "pk3s.log"), a.Redactor)
	if err != nil {
		return err
	}
	a.Log = l
	if ex, ok := a.R.(runner.Exec); ok {
		ex.Log = func(line string) { l.Printf("%s", line) }
		a.R = ex
	}
	return nil
}

// New creates an App executing real commands. Call OpenLog to enable the
// activity log.
func New(root string) *App {
	return &App{Root: root, R: runner.Exec{}, Fetch: k3s.NewFetcher(), Redactor: &runner.Redactor{}, LiveProbes: true}
}

// Inventory collects host inventory.
func (a *App) Inventory(ctx context.Context) (*pve.Inventory, error) {
	inv, err := pve.Client{R: a.R}.Collect(ctx)
	if err != nil {
		a.Logf("inventory failed: %v", err)
		return nil, err
	}
	a.Logf("inventory: node %s, PVE %s, kernel %s, %s; %d storages, %d bridges, %d templates, %d guests, firewall=%v",
		inv.Node, inv.PVEVersion, inv.Kernel, inv.Arch, len(inv.Storages), len(inv.Bridges), len(inv.Templates), len(inv.Guests), inv.FirewallOn)
	return inv, nil
}

// ApplyInventoryDefaults fills environment-specific blanks in a spec from
// the inventory (first matching storage, bridge with an address, ...).
func ApplyInventoryDefaults(s *spec.ClusterSpec, inv *pve.Inventory) {
	if s.Node == "" {
		s.Node = inv.Node
	}
	if s.RootfsStorage == "" {
		for _, st := range inv.Storages {
			if st.Active && st.Has("rootdir") {
				s.RootfsStorage = st.ID
				break
			}
		}
	}
	if s.Template.Storage == "" {
		for _, st := range inv.Storages {
			if st.Active && st.Has("vztmpl") {
				s.Template.Storage = st.ID
				break
			}
		}
	}
	if s.Template.Name == "" {
		// Prefer the newest Debian standard template already present.
		best := ""
		for _, t := range inv.Templates {
			if strings.HasPrefix(t.VolID, s.Template.Storage+":") && strings.HasPrefix(t.Name(), "debian-") && t.Name() > best {
				best = t.Name()
			}
		}
		s.Template.Name = best
	}
	if br, ok := inv.Bridge(s.Network.Bridge); ok {
		if s.Network.Gateway == "" {
			s.Network.Gateway = br.Gateway
		}
		if p := br.Prefix(); p > 0 && s.Network.Gateway == br.Gateway {
			s.Network.Prefix = p
		}
	}
	if len(s.Network.DNS) == 0 {
		s.Network.DNS = append([]string{}, inv.HostDNS...)
	}
	if s.Network.SearchDomain == "" {
		s.Network.SearchDomain = inv.HostSearch
	}
	if s.Access.Timezone == "host" && inv.HostTimezone == "" {
		s.Access.Timezone = ""
	}
}

// ResolveVersion turns a channel into an exact version.
func (a *App) ResolveVersion(ctx context.Context, v string) (string, error) {
	if k3s.IsExactVersion(v) {
		return v, nil
	}
	return a.Fetch.ResolveChannel(ctx, v)
}

// Prepared is a validated plan ready to apply.
type Prepared struct {
	Plan     *plan.Plan
	Findings validate.Findings
	Inv      *pve.Inventory
}

// Prepare builds and validates a plan for a new cluster.
// If pk3s already manages a cluster with this name, the result is an
// update plan (diff against the deployed cluster) instead.
func (a *App) Prepare(ctx context.Context, s spec.ClusterSpec, inv *pve.Inventory) (*Prepared, error) {
	prep, err := a.prepare(ctx, s, inv)
	a.logPrepared(s, prep, err)
	return prep, err
}

func (a *App) logPrepared(s spec.ClusterSpec, prep *Prepared, err error) {
	if a.Log == nil {
		return
	}
	if err != nil {
		a.Logf("plan %q failed: %v", s.Name, err)
		return
	}
	if data, merr := spec.Marshal(s); merr == nil {
		a.Logf("plan requested for cluster %q with spec:\n%s", s.Name, data)
	}
	if p := prep.Plan; p != nil {
		a.Logf("plan %q: mode %s, k3s %s, %d nodes, %d steps, changes: %v", s.Name, p.Mode, p.K3sVersion, len(p.Nodes), len(p.Steps), p.Changes)
	}
	for _, f := range prep.Findings {
		a.Logf("validation %s", f)
	}
}

func (a *App) prepare(ctx context.Context, s spec.ClusterSpec, inv *pve.Inventory) (*Prepared, error) {
	store := &state.Store{Root: a.Root, Cluster: s.Name}
	recovered := false
	if !store.Exists() {
		// No state on this host: if the cluster exists (tagged containers),
		// rebuild the state from the ConfigMap kept inside the cluster.
		ok, err := a.recoverState(ctx, s.Name, inv)
		if err != nil {
			return nil, err
		}
		recovered = ok
	}
	if store.Exists() {
		prep, err := a.prepareUpdate(ctx, s, inv)
		if err == nil && recovered {
			prep.Findings = append(prep.Findings, validate.Finding{Severity: validate.Info, Field: "name",
				Message: "state restored from ConfigMap kube-system/pk3s-state into " + store.Dir()})
		}
		return prep, err
	}
	f := validate.Spec(s, inv)
	if f.HasErrors() {
		return &Prepared{Findings: f, Inv: inv}, nil
	}
	ver, err := a.ResolveVersion(ctx, s.K3s.Version)
	if err != nil {
		return nil, err
	}
	p, err := plan.Build(s, inv, ver)
	if err != nil {
		return &Prepared{Findings: append(f, validate.Finding{Severity: validate.Error, Field: "network", Message: err.Error()}), Inv: inv}, nil
	}
	probes := validate.Probes{StateExists: func(name string) bool {
		st := &state.Store{Root: a.Root, Cluster: name}
		return st.Exists()
	}}
	if a.LiveProbes {
		probes.IPInUse = pingAlive
		probes.DirNonEmpty = dirNonEmpty
	}
	f = append(f, validate.Plan(p, inv, probes)...)
	return &Prepared{Plan: p, Findings: f, Inv: inv}, nil
}

func pingAlive(ip string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "ping", "-c", "1", "-W", "1", ip).Run() == nil
}

func dirNonEmpty(p string) bool {
	entries, err := os.ReadDir(p)
	return err == nil && len(entries) > 0
}

// Session is an engine bound to a cluster store (locked).
type Session struct {
	Engine *engine.Engine
	Store  *state.Store
}

// Run executes the plan. When every step has finished, the node layout is
// recorded and, for an update, the target spec and version become the
// deployed ones.
func (s *Session) Run(ctx context.Context) error {
	if err := s.Engine.Run(ctx); err != nil {
		return err
	}
	st, p := s.Engine.State, s.Engine.Plan
	st.Layout = p.Records()
	if p.Mode == plan.ModeUpdate {
		if err := spec.Save(s.Store.Path("spec.yaml"), p.Spec); err != nil {
			return err
		}
		st.K3sVersion = p.K3sVersion
		st.Pending = nil
	}
	return s.Store.Save(st)
}

// Close releases the store lock.
func (s *Session) Close() { s.Store.Unlock() }

// NewSession creates state for a new cluster (or opens existing state when
// resume is true) and returns an engine ready to Run.
func (a *App) NewSession(p *plan.Plan, resume bool, notify func(engine.Event)) (*Session, error) {
	store := &state.Store{Root: a.Root, Cluster: p.Spec.Name}
	if err := store.Init(); err != nil {
		return nil, err
	}
	if err := store.Lock(); err != nil {
		return nil, err
	}
	var st *state.State
	var err error
	switch {
	case resume:
		st, err = store.Load()
	case p.Mode == plan.ModeUpdate:
		st, err = store.Load()
		if err == nil && st.Pending != nil {
			err = fmt.Errorf("an update of %q is already in progress; run: pk3s resume %s", p.Spec.Name, p.Spec.Name)
		}
		if err == nil {
			st.Pending = &state.Operation{ID: p.OpID, Started: time.Now().UTC(), FromVersion: p.FromVersion, ToVersion: p.K3sVersion, Target: p.Spec}
			err = store.Save(st)
		}
	default:
		if store.Exists() {
			store.Unlock()
			return nil, fmt.Errorf("state for %q already exists; use resume", p.Spec.Name)
		}
		st = state.New(p.Spec.Name, p.K3sVersion)
		st.Layout = p.Records()
		if err = spec.Save(store.Path("spec.yaml"), p.Spec); err == nil {
			err = store.Save(st)
		}
	}
	if err != nil {
		store.Unlock()
		return nil, err
	}
	sec, err := store.LoadSecrets()
	if err != nil {
		store.Unlock()
		return nil, err
	}
	logFile, _ := os.OpenFile(store.Path("pk3s.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	eng := &engine.Engine{
		Plan: p, Store: store, State: st, Secrets: sec,
		R: a.R, PCT: pve.PCT{R: a.R}, Pveam: pve.Pveam{R: a.R},
		Fetch: a.Fetch, FS: engine.OSFS{}, Redactor: a.Redactor,
		Notify: func(ev engine.Event) {
			if logFile != nil {
				fmt.Fprintf(logFile, "%s %s\n", time.Now().UTC().Format(time.RFC3339), engine.FormatEvent(ev))
			}
			a.Logf("[%s] %s", p.Spec.Name, engine.FormatEvent(ev))
			if notify != nil {
				notify(ev)
			}
		},
	}
	a.Logf("session start: cluster %q, mode %s, resume=%v, k3s %s, %d steps", p.Spec.Name, p.Mode, resume, p.K3sVersion, len(p.Steps))
	return &Session{Engine: eng, Store: store}, nil
}

// LoadForResume rebuilds the plan of an existing cluster from its saved
// spec and pinned version.
func (a *App) LoadForResume(ctx context.Context, name string) (*plan.Plan, error) {
	store := &state.Store{Root: a.Root, Cluster: name}
	if !store.Exists() {
		return nil, a.NoStateError(name)
	}
	st, err := store.Load()
	if err != nil {
		return nil, fmt.Errorf("load state for %q: %w", name, err)
	}
	s, err := spec.Load(store.Path("spec.yaml"))
	if err != nil {
		return nil, err
	}
	inv, err := a.Inventory(ctx)
	if err != nil {
		return nil, err
	}
	if op := st.Pending; op != nil {
		return plan.BuildUpdate(plan.UpdateInput{
			Current: s, Layout: st.Layout, FromVersion: op.FromVersion,
			Target: op.Target, ToVersion: op.ToVersion, OpID: op.ID,
		}, inv)
	}
	return plan.BuildLayout(s, inv, st.K3sVersion, st.Layout)
}

// LoadSpec returns the deployed spec of a cluster.
func (a *App) LoadSpec(name string) (spec.ClusterSpec, error) {
	return spec.Load((&state.Store{Root: a.Root, Cluster: name}).Path("spec.yaml"))
}

// Clusters lists clusters with pk3s state.
func (a *App) Clusters() ([]string, error) { return state.List(a.Root) }

// StateRoot returns the state directory.
func (a *App) StateRoot() string { return a.Root }
