package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/oleksdovz/pk3s/internal/k3s"
	"github.com/oleksdovz/pk3s/internal/plan"
	"github.com/oleksdovz/pk3s/internal/pve"
	"github.com/oleksdovz/pk3s/internal/spec"
	"github.com/oleksdovz/pk3s/internal/state"
)

// NodeReport combines container and Kubernetes status for one node.
type NodeReport struct {
	Node     plan.Node
	CTStatus string // running | stopped | missing
	K8sReady string // Ready | NotReady | - (unknown)
	K8sVer   string
}

// StatusReport is the result of Status.
type StatusReport struct {
	Spec       spec.ClusterSpec
	K3sVersion string
	Nodes      []NodeReport
	Steps      map[string]state.StepState
	StepOrder  []plan.Step
	Kubeconfig string
	K8sError   string
}

// Status reports the current state of a cluster managed by pk3s.
func (a *App) Status(ctx context.Context, name string) (*StatusReport, error) {
	store := &state.Store{Root: a.Root, Cluster: name}
	st, err := store.Load()
	if err != nil {
		return nil, fmt.Errorf("no pk3s state for %q: %w", name, err)
	}
	s, err := spec.Load(store.Path("spec.yaml"))
	if err != nil {
		return nil, err
	}
	// Rebuild the plan without querying the host. Steps that only exist
	// when something was missing at deploy time (template download) are
	// counted only if they were recorded in the state.
	inv := &pve.Inventory{LoadedModules: map[string]bool{}}
	if _, ok := st.Steps[string(plan.StepTemplate)]; !ok {
		inv.Templates = []pve.Template{{VolID: s.Template.VolID()}}
	}
	p, err := plan.BuildLayout(s, inv, st.K3sVersion, st.Layout)
	if err != nil {
		return nil, err
	}
	rep := &StatusReport{Spec: s, K3sVersion: st.K3sVersion, Steps: st.Steps, StepOrder: p.Steps}
	if _, err := os.Stat(store.Path("kubeconfig")); err == nil {
		rep.Kubeconfig = store.Path("kubeconfig")
	}
	pct := pve.PCT{R: a.R}
	k8s := map[string]k3s.NodeStatus{}
	if status, err := pct.Status(ctx, p.Nodes[0].CTID); err == nil && status == "running" {
		out, err := pct.Exec(ctx, p.Nodes[0].CTID, k3s.BinaryPath, "kubectl", "get", "nodes", "-o", "json")
		if err == nil {
			nodes, perr := k3s.ParseNodes([]byte(out))
			if perr == nil {
				for _, n := range nodes {
					k8s[n.Name] = n
				}
			}
		} else {
			rep.K8sError = "kubectl on bootstrap server failed"
		}
	}
	for _, n := range p.Nodes {
		r := NodeReport{Node: n, CTStatus: "missing", K8sReady: "-"}
		if !st.HasResource(state.KindContainer, strconv.Itoa(n.CTID)) {
			r.CTStatus = "not created"
		} else if status, err := pct.Status(ctx, n.CTID); err == nil {
			r.CTStatus = status
		}
		if ns, ok := k8s[n.Hostname]; ok {
			r.K8sReady = "NotReady"
			if ns.Ready {
				r.K8sReady = "Ready"
			}
			r.K8sVer = ns.Version
		}
		rep.Nodes = append(rep.Nodes, r)
	}
	return rep, nil
}

// DestroyItem is one container that destroy would remove.
type DestroyItem struct {
	CTID   int
	Exists bool
	Owned  bool // carries the cluster tag
	Reason string
}

// DestroyPlan lists what destroy would do. Nothing is changed.
type DestroyPlan struct {
	Cluster    string
	Containers []DestroyItem
	// Kept are resources pk3s created but never deletes automatically.
	Kept []state.Resource
}

// Blocked reports whether a container in the ledger fails ownership checks.
func (d *DestroyPlan) Blocked() bool {
	for _, c := range d.Containers {
		if c.Exists && !c.Owned {
			return true
		}
	}
	return false
}

// PlanDestroy inspects the ledger and verifies ownership of each container.
func (a *App) PlanDestroy(ctx context.Context, name string) (*DestroyPlan, error) {
	store := &state.Store{Root: a.Root, Cluster: name}
	st, err := store.Load()
	if err != nil {
		return nil, fmt.Errorf("no pk3s state for %q: %w", name, err)
	}
	pct := pve.PCT{R: a.R}
	dp := &DestroyPlan{Cluster: name}
	for _, r := range st.Resources {
		if r.Kind != state.KindContainer {
			dp.Kept = append(dp.Kept, r)
			continue
		}
		id, err := strconv.Atoi(r.ID)
		if err != nil {
			continue
		}
		item := DestroyItem{CTID: id}
		if _, err := pct.Status(ctx, id); err != nil {
			item.Reason = "already gone"
		} else {
			item.Exists = true
			conf, err := pct.ReadConfig(ctx, id)
			if err == nil && hasTag(conf, plan.ClusterTag(name)) {
				item.Owned = true
			} else {
				item.Reason = fmt.Sprintf("does not carry tag %s; it may have been replaced. Refusing to destroy", plan.ClusterTag(name))
			}
		}
		dp.Containers = append(dp.Containers, item)
	}
	// Destroy agents before servers (reverse creation order).
	sort.Slice(dp.Containers, func(i, j int) bool { return dp.Containers[i].CTID > dp.Containers[j].CTID })
	return dp, nil
}

func hasTag(conf, tag string) bool {
	for _, line := range strings.Split(conf, "\n") {
		if strings.HasPrefix(line, "[") {
			break
		}
		if v, ok := strings.CutPrefix(line, "tags:"); ok {
			for _, t := range pve.SplitTags(strings.TrimSpace(v)) {
				if t == tag {
					return true
				}
			}
		}
	}
	return false
}

// Destroy removes the containers in dp (after the caller confirmed). It
// never deletes data directories, host files or templates. When every
// container is gone, credentials are deleted and the state directory is
// archived so the cluster name can be reused.
func (a *App) Destroy(ctx context.Context, dp *DestroyPlan, progress func(string)) error {
	if dp.Blocked() {
		return fmt.Errorf("destroy blocked: some containers failed ownership checks")
	}
	store := &state.Store{Root: a.Root, Cluster: dp.Cluster}
	if err := store.Lock(); err != nil {
		return err
	}
	defer store.Unlock()
	st, err := store.Load()
	if err != nil {
		return err
	}
	pct := pve.PCT{R: a.R}
	for _, c := range dp.Containers {
		if c.Exists {
			progress(fmt.Sprintf("stopping and destroying CT %d", c.CTID))
			if err := pct.Stop(ctx, c.CTID); err != nil {
				return err
			}
			if err := pct.Destroy(ctx, c.CTID); err != nil {
				return err
			}
		}
		st.RemoveResource(state.KindContainer, strconv.Itoa(c.CTID))
		if err := store.Save(st); err != nil {
			return err
		}
	}
	for _, f := range []string{"secrets.json", "kubeconfig"} {
		_ = os.Remove(store.Path(f))
	}
	_ = os.RemoveAll(store.Path("tmp"))
	archive := filepath.Join(a.Root, "clusters", ".destroyed", fmt.Sprintf("%s-%s", dp.Cluster, time.Now().UTC().Format("20060102T150405Z")))
	if err := os.MkdirAll(filepath.Dir(archive), 0o700); err != nil {
		return err
	}
	if err := os.Rename(store.Dir(), archive); err != nil {
		return err
	}
	progress("state archived to " + archive)
	return nil
}

// RecoveryHint explains how to continue after a failure.
func RecoveryHint(name, root string) string {
	return fmt.Sprintf(`Progress is saved in %[2]s/clusters/%[1]s/state.json.
  * Fix the cause shown above, then resume:   pk3s resume %[1]s
  * Inspect what exists:                       pk3s status %[1]s
  * Remove containers created by pk3s:         pk3s destroy %[1]s   (asks for confirmation)
Existing guests that pk3s did not create are never modified or removed.`, name, root)
}
