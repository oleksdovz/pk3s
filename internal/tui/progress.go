package tui

import (
	"fmt"

	"github.com/oleksdovz/pk3s/internal/plan"
	"github.com/oleksdovz/pk3s/internal/state"
)

// progressRow is one line of the running screen: a host step, a container
// (all steps of that container combined) or a cluster-wide step.
type progressRow struct {
	section string // "Host", "Containers", "Cluster"
	icon    string // status icon key: done, running, failed, waiting, pending
	label   string
	detail  string
}

// phase labels per step kind: while running / when done.
var phases = map[plan.StepKind][2]string{
	plan.StepProvision:   {"provisioning: create, start, OS packages", "provisioned"},
	plan.StepBootstrap:   {"bootstrapping k3s server", "k3s server up"},
	plan.StepJoin:        {"joining the cluster", "ready"},
	plan.StepUpdateCT:    {"updating CPU/RAM/disk/autostart", "settings updated"},
	plan.StepUpgradeNode: {"upgrading k3s", "upgraded"},
	plan.StepRemoveNode:  {"cordon, drain, delete node, destroy", "removed"},
	// Steps of plans made by older builds (resume).
	plan.StepCreateCT: {"creating container", "created"}, plan.StepConfigureCT: {"applying LXC profile", "configured"},
	plan.StepStartCT: {"starting", "started"}, plan.StepPrepareNode: {"installing OS packages", "prepared"},
	plan.StepInstallK3s: {"installing k3s", "k3s installed"}, plan.StepWaitNode: {"waiting for k3s", "ready"},
}

// stepHost returns the container a step belongs to ("" for host/cluster steps).
func stepHost(p *plan.Plan, st plan.Step) (string, int) {
	switch {
	case st.Kind == plan.StepRemoveNode && st.Node >= 0 && st.Node < len(p.Removed):
		n := p.Removed[st.Node]
		return n.Hostname, n.CTID
	case st.Node >= 0 && st.Node < len(p.Nodes):
		n := p.Nodes[st.Node]
		return n.Hostname, n.CTID
	}
	return "", 0
}

// progressRows groups the plan's steps: host steps, one row per container
// with its current phase, then cluster-wide steps.
func (m Model) progressRows() []progressRow {
	p := m.plan
	var host, cluster []progressRow
	type agg struct {
		id    int
		steps []plan.Step
	}
	var order []string
	byHost := map[string]*agg{}
	seenNode := false
	for _, st := range p.Steps {
		h, id := stepHost(p, st)
		if h == "" {
			row := progressRow{icon: m.iconFor(st.ID), label: st.Title}
			if !seenNode {
				row.section = "Host"
				host = append(host, row)
			} else {
				row.section = "Cluster"
				cluster = append(cluster, row)
			}
			continue
		}
		seenNode = true
		if byHost[h] == nil {
			byHost[h] = &agg{id: id}
			order = append(order, h)
		}
		byHost[h].steps = append(byHost[h].steps, st)
	}
	rows := host
	for _, h := range order {
		a := byHost[h]
		row := progressRow{section: "Containers", label: fmt.Sprintf("%s (CT %d)", h, a.id), icon: "pending", detail: "waiting"}
		done := 0
		lastDone := ""
		for _, st := range a.steps {
			ph := phases[st.Kind]
			switch m.stepStatus[st.ID] {
			case state.StatusFailed:
				row.icon, row.detail = "failed", "failed: "+ph[0]
			case state.StatusRunning:
				if row.icon != "failed" {
					row.icon, row.detail = "running", ph[0]
				}
			case state.StatusDone, "skipped":
				done++
				lastDone = ph[1]
			}
		}
		switch {
		case row.icon == "failed" || row.icon == "running":
		case done == len(a.steps):
			row.icon, row.detail = "done", lastDone
		case done > 0:
			row.icon, row.detail = "waiting", lastDone+", waiting for the next phase"
		}
		rows = append(rows, row)
	}
	return append(rows, cluster...)
}

func (m Model) iconFor(stepID string) string {
	switch m.stepStatus[stepID] {
	case state.StatusDone, "skipped":
		return "done"
	case state.StatusRunning:
		return "running"
	case state.StatusFailed:
		return "failed"
	}
	return "pending"
}

func (m Model) renderIcon(key string) string {
	switch key {
	case "done":
		return stOK.Render("✓")
	case "running":
		return stTitle.Render(m.spin.View())
	case "failed":
		return stErr.Render("✗")
	case "waiting":
		return stDim.Render("·")
	}
	return " "
}
