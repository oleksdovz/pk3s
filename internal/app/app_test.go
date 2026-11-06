package app

import (
	"context"
	"strings"
	"testing"

	"github.com/oleksdovz/pk3s/internal/plan"
	"github.com/oleksdovz/pk3s/internal/runner"
	"github.com/oleksdovz/pk3s/internal/spec"
	"github.com/oleksdovz/pk3s/internal/state"
	"github.com/oleksdovz/pk3s/internal/testutil"
)

func deployedSpec() spec.ClusterSpec {
	s := spec.Default()
	s.Name = "lab"
	s.Servers, s.Agents = 1, 2
	s.K3s.Version = "v1.37.2+k3s1"
	s.Server.MemoryMiB = 4096
	s.Template = spec.Template{Storage: "local", Name: "debian-13-standard_13.1-2_amd64.tar.zst"}
	s.RootfsStorage = "x-data"
	s.Network = spec.Network{Bridge: "vmbr0", FirstIP: "192.168.0.52", Prefix: 24, Gateway: "192.168.0.1", DNS: []string{"192.168.0.2"}}
	s.Security.AcknowledgePrivilegedRisk = true
	return s
}

// seed writes state as if the cluster had been deployed by pk3s.
func seed(t *testing.T, root string, s spec.ClusterSpec) {
	t.Helper()
	p, err := plan.Build(s, testutil.HPInventory(t), "v1.37.2+k3s1")
	if err != nil {
		t.Fatal(err)
	}
	store := &state.Store{Root: root, Cluster: s.Name}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	st := state.New(s.Name, "v1.37.2+k3s1")
	st.Layout = p.Records()
	if err := store.Save(st); err != nil {
		t.Fatal(err)
	}
	if err := spec.Save(store.Path("spec.yaml"), s); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareDetectsExistingClusterAndCommits(t *testing.T) {
	root := t.TempDir()
	cur := deployedSpec()
	seed(t, root, cur)
	a := &App{Root: root, R: runner.NewFake(), Redactor: &runner.Redactor{}}
	inv := testutil.HPInventory(t)

	tgt := cur
	tgt.Agents = 3
	prep, err := a.Prepare(context.Background(), tgt, inv)
	if err != nil {
		t.Fatal(err)
	}
	if prep.Findings.HasErrors() {
		t.Fatalf("errors: %v", prep.Findings)
	}
	p := prep.Plan
	if p.Mode != plan.ModeUpdate || len(p.Changes) != 1 || !strings.Contains(p.Changes[0], "lab-node3") {
		t.Fatalf("expected update adding lab-node3: %+v", p.Changes)
	}

	sess, err := a.NewSession(p, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := sess.Store.Load()
	if st.Pending == nil || st.Pending.Target.Agents != 3 {
		t.Fatal("pending operation not recorded before running")
	}
	// A second update must be refused while one is pending.
	if _, err := a.Prepare(context.Background(), tgt, inv); err == nil || !strings.Contains(err.Error(), "in progress") {
		t.Fatalf("expected in-progress error, got %v", err)
	}
	// Skip the engine work; verify the commit at the end of Run.
	sess.Engine.Plan.Steps = nil
	if err := sess.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	sess.Close()
	st, _ = sess.Store.Load()
	if st.Pending != nil || len(st.Layout) != 4 {
		t.Fatalf("commit failed: pending=%v layout=%d", st.Pending, len(st.Layout))
	}
	saved, _ := a.LoadSpec("lab")
	if saved.Agents != 3 {
		t.Fatalf("spec.yaml not updated: %d", saved.Agents)
	}
	// Applying the same spec again is a no-op.
	prep, err = a.Prepare(context.Background(), tgt, inv)
	if err != nil || len(prep.Plan.Changes) != 0 {
		t.Fatalf("expected no changes: %v %v", err, prep.Plan.Changes)
	}
}

func TestChannelDoesNotUpgrade(t *testing.T) {
	root := t.TempDir()
	cur := deployedSpec()
	seed(t, root, cur)
	a := &App{Root: root, R: runner.NewFake(), Redactor: &runner.Redactor{}}
	tgt := cur
	tgt.K3s.Version = "stable"
	prep, err := a.Prepare(context.Background(), tgt, testutil.HPInventory(t))
	if err != nil {
		t.Fatal(err)
	}
	if prep.Plan.K3sVersion != "v1.37.2+k3s1" || len(prep.Plan.Changes) != 0 {
		t.Fatalf("channel must not upgrade: %s %v", prep.Plan.K3sVersion, prep.Plan.Changes)
	}
}
