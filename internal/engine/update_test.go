package engine_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/oleksdovz/pk3s/internal/k3s"
	"github.com/oleksdovz/pk3s/internal/plan"
	"github.com/oleksdovz/pk3s/internal/spec"
	"github.com/oleksdovz/pk3s/internal/testutil"
)

// deployed returns a harness with a finished 3 main + 2 node cluster.
func deployed(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, nil)
	if err := h.eng.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	return h
}

// update switches the harness to an update plan and runs it.
func (h *harness) update(t *testing.T, mutate func(*spec.ClusterSpec), toVersion string) *plan.Plan {
	t.Helper()
	cur := h.eng.Plan.Spec
	tgt := cur
	tgt.NodeNetwork = slices.Clone(cur.NodeNetwork)
	mutate(&tgt)
	from := h.eng.Plan.K3sVersion
	if toVersion == "" {
		toVersion = from
	}
	p, err := plan.BuildUpdate(plan.UpdateInput{
		Current: cur, Layout: h.eng.Plan.Records(), FromVersion: from,
		Target: tgt, ToVersion: toVersion, OpID: "op-1",
	}, testutil.HPInventory(t))
	if err != nil {
		t.Fatal(err)
	}
	st, _ := h.store.Load()
	sec, _ := h.store.LoadSecrets()
	h.sim.calls = nil
	h.eng = h.newEngine(p, st, sec, h.eng.Fetch.(fakeFetch).dir)
	if err := h.eng.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	return p
}

func (h *harness) callLines() []string {
	var out []string
	for _, c := range h.sim.calls {
		out = append(out, strings.Join(append([]string{c.Name}, c.Args...), " "))
	}
	return out
}

func TestScaleUpAgent(t *testing.T) {
	h := deployed(t)
	p := h.update(t, func(s *spec.ClusterSpec) { s.Agents = 3 }, "")
	if len(p.Changes) != 1 || !strings.Contains(p.Changes[0], "add agent lab-node3 (CT 5005, 192.168.0.56)") {
		t.Fatalf("changes: %v", p.Changes)
	}
	ct := h.sim.cts[5005]
	if ct == nil || !ct.k3s {
		t.Fatal("new agent CT 5005 not created/started")
	}
	if !strings.Contains(ct.files[k3s.ConfigPath], "token: "+secureAgent) {
		t.Error("new agent must join with the agent token")
	}
	for _, l := range h.callLines() {
		for _, id := range []string{"5000", "5001", "5002", "5003", "5004"} {
			if strings.HasPrefix(l, "pct stop "+id) || strings.HasPrefix(l, "pct set "+id) || strings.HasPrefix(l, "pct push "+id) {
				t.Errorf("existing CT touched: %s", l)
			}
		}
	}
}

func TestScaleDownRemovesHighestAgent(t *testing.T) {
	h := deployed(t)
	p := h.update(t, func(s *spec.ClusterSpec) { s.Agents = 1 }, "")
	if len(p.Removed) != 1 || p.Removed[0].CTID != 5004 {
		t.Fatalf("expected CT 5004 to be removed, got %+v", p.Removed)
	}
	if _, ok := h.sim.cts[5004]; ok {
		t.Error("CT 5004 still exists")
	}
	if _, ok := h.sim.cts[5003]; !ok {
		t.Error("CT 5003 must be kept")
	}
	want := []string{"drain lab-node2 --ignore-daemonsets --delete-emptydir-data --timeout=5m0s", "delete node lab-node2 --ignore-not-found"}
	if !slices.Equal(h.sim.kubectl, want) {
		t.Errorf("kubectl calls: %v", h.sim.kubectl)
	}
	st, _ := h.store.Load()
	if st.HasResource("container", "5004") || !st.HasResource("container", "5003") {
		t.Error("ledger not updated")
	}
	if !strings.Contains(strings.Join(p.Changes, "\n"), "data directory /data/k3s-storage/lab/lab-node2 is kept") {
		t.Errorf("changes should mention kept data dir: %v", p.Changes)
	}
}

func TestRollingUpgrade(t *testing.T) {
	h := deployed(t)
	h.update(t, func(s *spec.ClusterSpec) {}, "v1.37.3+k3s1")
	var order []string
	for _, l := range h.callLines() {
		if strings.HasPrefix(l, "pct exec") && strings.Contains(l, "mv -f") {
			order = append(order, strings.Fields(l)[2])
		}
	}
	if !slices.Equal(order, []string{"5000", "5001", "5002", "5003", "5004"}) {
		t.Errorf("upgrade order: %v", order)
	}
	k := h.sim.kubectl
	if !strings.HasPrefix(k[0], "etcd-snapshot pk3s-pre-v1-37-3-k3s1") {
		t.Errorf("snapshot must come first: %v", k)
	}
	if !slices.Contains(k, "cordon lab-main1") || slices.ContainsFunc(k, func(s string) bool { return strings.HasPrefix(s, "drain lab-main") }) {
		t.Errorf("servers are cordoned, not drained: %v", k)
	}
	if !slices.ContainsFunc(k, func(s string) bool { return strings.HasPrefix(s, "drain lab-node1") }) || !slices.Contains(k, "uncordon lab-node1") {
		t.Errorf("agents are drained and uncordoned: %v", k)
	}
	for id, ct := range h.sim.cts {
		if ct.files[k3s.BinaryPath] != "v1.37.3+k3s1" {
			t.Errorf("CT %d runs %q", id, ct.files[k3s.BinaryPath])
		}
	}
}

func TestResourceChange(t *testing.T) {
	h := deployed(t)
	p := h.update(t, func(s *spec.ClusterSpec) { s.Agent.MemoryMiB = 4096; s.Agent.DiskGiB = 40 }, "")
	if len(p.Changes) != 2 {
		t.Fatalf("expected changes for 2 agents: %v", p.Changes)
	}
	lines := strings.Join(h.callLines(), "\n")
	for _, want := range []string{"pct set 5003 --cores 2 --memory 4096", "pct resize 5003 rootfs 40G", "pct resize 5004 rootfs 40G"} {
		if !strings.Contains(lines, want) {
			t.Errorf("missing %q in:\n%s", want, lines)
		}
	}
	if strings.Contains(lines, "pct set 5000") {
		t.Error("servers did not change and must not be touched")
	}
}

func TestNoChangeProducesNoSteps(t *testing.T) {
	h := deployed(t)
	p := h.update(t, func(s *spec.ClusterSpec) {}, "")
	if len(p.Changes) != 0 || len(p.Steps) != 0 {
		t.Fatalf("expected empty plan: %v %v", p.Changes, p.Steps)
	}
}
