package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oleksdovz/pk3s/internal/k3s"
	"github.com/oleksdovz/pk3s/internal/pve"

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

func TestRecoverStateFromConfigMap(t *testing.T) {
	// Cluster deployed elsewhere: no host state, containers carry the tag
	// and main1 holds the ConfigMap.
	cur := deployedSpec()
	inv := testutil.HPInventory(t)
	p, err := plan.Build(cur, inv, "v1.37.2+k3s1")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range p.Nodes {
		inv.Guests = append(inv.Guests, pve.Guest{VMID: n.CTID, Name: n.Hostname, Type: "lxc", Node: "hp", Status: "running", Tags: []string{"pk3s", "pk3s-lab"}, IPs: []string{n.IP}})
	}
	specYAML, _ := spec.Marshal(cur)
	layout, _ := json.Marshal(p.Records())
	cm, _ := k3s.StateConfigMap(k3s.ClusterState{SpecYAML: string(specYAML), LayoutJSON: string(layout), K3sVersion: "v1.37.2+k3s1"})

	f := runner.NewFake()
	f.OK("pct exec 5000 -- /usr/local/bin/k3s kubectl get configmap pk3s-state -n kube-system -o json", string(cm))
	f.OK("pct exec 5000 -- cat /etc/rancher/k3s/config.yaml", "token: rawserver0123456789\nagent-token: rawagent0123456789\n")
	f.OK("pct exec 5000 -- cat /var/lib/rancher/k3s/server/token", "K10abc::server:rawserver0123456789")
	f.OK("pct exec 5000 -- cat /var/lib/rancher/k3s/server/agent-token", "K10abc::node:rawagent0123456789")
	f.OK("pct exec 5000 -- cat /etc/rancher/k3s/k3s.yaml", "server: https://127.0.0.1:6443\ncurrent-context: default")

	root := t.TempDir()
	a := &App{Root: root, R: f, Redactor: &runner.Redactor{}}
	tgt := cur
	tgt.Agents = 3
	prep, err := a.Prepare(context.Background(), tgt, inv)
	if err != nil {
		t.Fatal(err)
	}
	if prep.Findings.HasErrors() || prep.Plan.Mode != plan.ModeUpdate {
		t.Fatalf("expected update plan: %v", prep.Findings)
	}
	if !strings.Contains(prep.Findings.String(), "state restored from ConfigMap kube-system/pk3s-state") {
		t.Error("recovery not reported")
	}
	if len(prep.Plan.Changes) != 1 || !strings.Contains(prep.Plan.Changes[0], "lab-node3") {
		t.Errorf("changes: %v", prep.Plan.Changes)
	}
	store := &state.Store{Root: root, Cluster: "lab"}
	sec, _ := store.LoadSecrets()
	if sec.AgentToken != "rawagent0123456789" || sec.SecureAgentToken == "" {
		t.Errorf("tokens not recovered: %+v", sec)
	}
	st, _ := store.Load()
	if len(st.Layout) != 3 || !st.HasResource(state.KindContainer, "5002") {
		t.Errorf("layout/ledger not recovered: %+v", st)
	}
}

func TestInitExportsExistingCluster(t *testing.T) {
	root := t.TempDir()
	cur := deployedSpec()
	seed(t, root, cur)
	a := &App{Root: root, R: testutil.HPFake(t), Redactor: &runner.Redactor{}}
	res, err := a.InitSpec(context.Background(), "lab", "test")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Existing || !strings.Contains(res.Content, `Deployed spec of cluster "lab"`) {
		t.Fatalf("expected deployed spec export:\n%s", res.Content)
	}
	back, err := spec.Parse([]byte(res.Content))
	if err != nil || back.Agents != cur.Agents || back.Network.FirstIP != cur.Network.FirstIP {
		t.Fatalf("exported spec wrong: %v %+v", err, back)
	}
}

func TestNoStateErrorExplainsDestroyedCluster(t *testing.T) {
	root := t.TempDir()
	a := &App{Root: root, R: runner.NewFake(), Redactor: &runner.Redactor{}}
	if err := a.NoStateError("lab"); !strings.Contains(err.Error(), "no clusters yet") {
		t.Fatal(err)
	}
	seed(t, root, deployedSpec())
	if err := a.NoStateError("other"); !strings.Contains(err.Error(), "known clusters: lab") {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "clusters", ".destroyed", "old-20261106T190901Z"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Status(context.Background(), "old"); err == nil || !strings.Contains(err.Error(), `cluster "old" was destroyed by pk3s at 2026-11-06`) {
		t.Fatal(err)
	}
}

func TestSessionCopiesKubeconfigLocally(t *testing.T) {
	root := t.TempDir()
	out := t.TempDir()
	cur := deployedSpec()
	seed(t, root, cur)
	store := &state.Store{Root: root, Cluster: "lab"}
	if _, err := store.WriteFile("kubeconfig", []byte("apiVersion: v1\n")); err != nil {
		t.Fatal(err)
	}
	a := &App{Root: root, R: runner.NewFake(), Redactor: &runner.Redactor{}, KubeconfigDir: out}
	tgt := cur
	tgt.Agents = 3
	prep, err := a.Prepare(context.Background(), tgt, testutil.HPInventory(t))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := a.NewSession(prep.Plan, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	sess.Engine.Plan.Steps = nil
	if err := sess.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(out, "lab-kubeconfig.yaml")
	fi, err := os.Stat(want)
	if err != nil || fi.Mode().Perm() != 0o600 || sess.LocalKubeconfig != want {
		t.Fatalf("kubeconfig not copied: %v %v %q", fi, err, sess.LocalKubeconfig)
	}
}
