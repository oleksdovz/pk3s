package plan_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/oleksdovz/pk3s/internal/plan"
	"github.com/oleksdovz/pk3s/internal/spec"
	"github.com/oleksdovz/pk3s/internal/testutil"
)

func deployedLayout(t *testing.T, s spec.ClusterSpec) []plan.NodeRecord {
	t.Helper()
	p, err := plan.Build(s, testutil.HPInventory(t), ver)
	if err != nil {
		t.Fatal(err)
	}
	return p.Records()
}

func TestAddServersKeepsExistingIDs(t *testing.T) {
	cur := baseSpec() // 3 main (5000-5002), 2 node (5003-5004), .51-.55
	tgt := cur
	tgt.Servers = 5
	p, err := plan.BuildUpdate(plan.UpdateInput{Current: cur, Layout: deployedLayout(t, cur), FromVersion: ver, Target: tgt, ToVersion: ver, OpID: "op"}, testutil.HPInventory(t))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, n := range p.Nodes {
		got[n.Hostname] = n.IP + "/" + itoa(n.CTID)
	}
	want := map[string]string{
		"lab-main1": "192.168.0.51/5000", "lab-main2": "192.168.0.52/5001", "lab-main3": "192.168.0.53/5002",
		"lab-main4": "192.168.0.56/5005", "lab-main5": "192.168.0.57/5006",
		"lab-node1": "192.168.0.54/5003", "lab-node2": "192.168.0.55/5004",
	}
	for h, w := range want {
		if got[h] != w {
			t.Errorf("%s = %s, want %s", h, got[h], w)
		}
	}
	// New servers join one by one after nothing else changes.
	var created []string
	for _, st := range p.Steps {
		if st.Kind == plan.StepCreateCT {
			created = append(created, p.Nodes[st.Node].Hostname)
		}
	}
	if strings.Join(created, ",") != "lab-main4,lab-main5" {
		t.Errorf("created: %v", created)
	}
	cfg := p.NodeConfig(p.Nodes[3], "", "", "K10x")
	if cfg.Server != "https://192.168.0.51:6443" || cfg.ClusterInit {
		t.Errorf("new server must join main1: %+v", cfg)
	}
}

func TestStepIDsAreScopedToOperation(t *testing.T) {
	cur := baseSpec()
	tgt := cur
	tgt.Agents = 3
	p, err := plan.BuildUpdate(plan.UpdateInput{Current: cur, Layout: deployedLayout(t, cur), FromVersion: ver, Target: tgt, ToVersion: ver, OpID: "op-42"}, testutil.HPInventory(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range p.Steps {
		if !strings.HasPrefix(st.ID, "op-42/") {
			t.Errorf("step %s not scoped", st.ID)
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
