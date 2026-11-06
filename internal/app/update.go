package app

import (
	"context"
	"fmt"
	"time"

	"github.com/oleksdovz/pk3s/internal/k3s"
	"github.com/oleksdovz/pk3s/internal/plan"
	"github.com/oleksdovz/pk3s/internal/pve"
	"github.com/oleksdovz/pk3s/internal/spec"
	"github.com/oleksdovz/pk3s/internal/state"
	"github.com/oleksdovz/pk3s/internal/validate"
)

// prepareUpdate builds the diff between the deployed cluster and target.
func (a *App) prepareUpdate(ctx context.Context, target spec.ClusterSpec, inv *pve.Inventory) (*Prepared, error) {
	store := &state.Store{Root: a.Root, Cluster: target.Name}
	st, err := store.Load()
	if err != nil {
		return nil, err
	}
	if st.Pending != nil {
		return nil, fmt.Errorf("an update of %q is already in progress; run: pk3s resume %s", target.Name, target.Name)
	}
	if len(st.Layout) == 0 {
		return nil, fmt.Errorf("cluster %q was created by an older pk3s without a node layout; it cannot be updated", target.Name)
	}
	current, err := spec.Load(store.Path("spec.yaml"))
	if err != nil {
		return nil, err
	}
	to := st.K3sVersion
	if k3s.IsExactVersion(target.K3s.Version) {
		to = target.K3s.Version
	}
	in := plan.UpdateInput{
		Current: current, Layout: st.Layout, FromVersion: st.K3sVersion,
		Target: target, ToVersion: to, OpID: "op-" + time.Now().UTC().Format("20060102T150405Z"),
	}
	f := validate.Spec(target, inv)
	f = append(f, validate.Update(in)...)
	if f.HasErrors() {
		return &Prepared{Findings: f, Inv: inv}, nil
	}
	p, err := plan.BuildUpdate(in, inv)
	if err != nil {
		return &Prepared{Findings: append(f, validate.Finding{Severity: validate.Error, Field: "node_network", Message: err.Error()}), Inv: inv}, nil
	}
	probes := validate.Probes{}
	if a.LiveProbes {
		probes.IPInUse = pingAlive
		probes.DirNonEmpty = dirNonEmpty
	}
	f = append(f, validate.Plan(p, inv, probes)...)
	if len(p.Changes) == 0 {
		f = append(f, validate.Finding{Severity: validate.Info, Field: "name", Message: "the deployed cluster already matches this spec; nothing to change"})
	}
	return &Prepared{Plan: p, Findings: f, Inv: inv}, nil
}

// UpdateInput returns the update description for editing a deployed
// cluster (used by the TUI for live validation; nothing is changed).
func (a *App) UpdateInput(name string, target spec.ClusterSpec) (plan.UpdateInput, error) {
	store := &state.Store{Root: a.Root, Cluster: name}
	st, err := store.Load()
	if err != nil {
		return plan.UpdateInput{}, err
	}
	current, err := spec.Load(store.Path("spec.yaml"))
	if err != nil {
		return plan.UpdateInput{}, err
	}
	to := st.K3sVersion
	if k3s.IsExactVersion(target.K3s.Version) {
		to = target.K3s.Version
	}
	return plan.UpdateInput{Current: current, Layout: st.Layout, FromVersion: st.K3sVersion, Target: target, ToVersion: to, OpID: "live"}, nil
}
