package report

import (
	"strings"
	"testing"

	"github.com/oleksdovz/pk3s/internal/app"
	"github.com/oleksdovz/pk3s/internal/plan"
	"github.com/oleksdovz/pk3s/internal/spec"
	"github.com/oleksdovz/pk3s/internal/state"
	"github.com/oleksdovz/pk3s/internal/testutil"
)

func TestUpdatePlanRendering(t *testing.T) {
	s := spec.Default()
	s.Name, s.Servers, s.Agents = "lab", 1, 4
	s.Template = spec.Template{Storage: "local", Name: "debian-13-standard_13.1-2_amd64.tar.zst"}
	s.RootfsStorage = "x-data"
	s.Network = spec.Network{Bridge: "vmbr0", FirstIP: "192.168.0.60", Prefix: 24, Gateway: "192.168.0.1"}
	inv := testutil.HPInventory(t)
	cur, err := plan.Build(s, inv, "v1.37.2+k3s1")
	if err != nil {
		t.Fatal(err)
	}
	tgt := s
	tgt.Agents = 3
	p, err := plan.BuildUpdate(plan.UpdateInput{Current: s, Layout: cur.Records(), FromVersion: "v1.37.2+k3s1", Target: tgt, ToVersion: "v1.37.3+k3s1", OpID: "op"}, inv)
	if err != nil {
		t.Fatal(err)
	}
	out := Plan(p)
	t.Log("\n" + out)
	for _, want := range []string{"UPDATE of the running cluster (k3s v1.37.2+k3s1 -> v1.37.3+k3s1)", "remove lab-node4 (CT 5004", "agent REMOVE", "upgrade k3s"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestStatusCountsLegacySteps(t *testing.T) {
	r := &app.StatusReport{
		Spec:      spec.ClusterSpec{Name: "old"},
		StepOrder: []plan.Step{{ID: "host-modules"}, {ID: "provision/old-main1"}},
		Steps: map[string]state.StepState{
			"host-modules":        {Status: state.StatusDone},
			"ct-create/old-main1": {Status: state.StatusDone},
			"kubeconfig":          {Status: state.StatusDone},
		},
	}
	if out := Status(r); !strings.Contains(out, "All operations completed.") {
		t.Fatal(out)
	}
}

func TestStatusShowsPendingAndFailures(t *testing.T) {
	r := &app.StatusReport{
		Spec:      spec.ClusterSpec{Name: "lab"},
		StepOrder: []plan.Step{{ID: "finalize"}},
		Steps: map[string]state.StepState{
			"finalize":                 {Status: state.StatusDone},
			"op-1/provision/lab-node3": {Status: state.StatusDone},
			"op-1/join/lab-node3":      {Status: state.StatusFailed, Error: "boom"},
		},
		Pending: &state.Operation{ID: "op-1", FromVersion: "v1", ToVersion: "v1"},
	}
	out := Status(r)
	if !strings.Contains(out, "Update op-1 in progress") || !strings.Contains(out, "1 step(s) done, 2 recorded") || !strings.Contains(out, "Last failure: op-1/join/lab-node3: boom") {
		t.Fatal(out)
	}
}
