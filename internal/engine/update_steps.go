package engine

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/oleksdovz/pk3s/internal/k3s"
	"github.com/oleksdovz/pk3s/internal/plan"
	"github.com/oleksdovz/pk3s/internal/pve"
	"github.com/oleksdovz/pk3s/internal/state"
)

// DrainTimeout bounds kubectl drain for agents.
const DrainTimeout = 5 * time.Minute

// kubectl runs kubectl on the bootstrap server.
func (e *Engine) kubectl(ctx context.Context, args ...string) (string, error) {
	return e.PCT.Exec(ctx, e.bootstrap().CTID, append([]string{k3s.BinaryPath, "kubectl"}, args...)...)
}

func (e *Engine) updateCT(ctx context.Context, n plan.Node) error {
	want := e.Plan.Record(n)
	if err := e.PCT.Set(ctx, n.CTID,
		"--cores", strconv.Itoa(want.Resources.Cores),
		"--memory", strconv.Itoa(want.Resources.MemoryMiB),
		"--startup", want.Startup,
		"--onboot", boolStr(want.OnBoot),
	); err != nil {
		return err
	}
	conf, err := e.PCT.ReadConfig(ctx, n.CTID)
	if err != nil {
		return err
	}
	if cur := pve.RootfsSizeGiB(conf); cur > 0 && want.Resources.DiskGiB > cur {
		e.logf("%s: growing rootfs %d -> %d GiB", n.Hostname, cur, want.Resources.DiskGiB)
		if err := e.PCT.Resize(ctx, n.CTID, "rootfs", want.Resources.DiskGiB); err != nil {
			return err
		}
	}
	return nil
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// backupDatastore takes an etcd snapshot (embedded etcd) or copies the
// SQLite database before an upgrade.
func (e *Engine) backupDatastore(ctx context.Context) error {
	b := e.bootstrap()
	name := "pk3s-pre-" + strings.NewReplacer("+", "-", ".", "-").Replace(e.Plan.K3sVersion)
	if e.Plan.Spec.EmbeddedEtcd() {
		if _, err := e.PCT.Exec(ctx, b.CTID, k3s.BinaryPath, "etcd-snapshot", "save", "--name", name); err != nil {
			return fmt.Errorf("etcd snapshot: %w", err)
		}
		e.logf("etcd snapshot %s saved in /var/lib/rancher/k3s/server/db/snapshots on %s", name, b.Hostname)
		return nil
	}
	dir := "/var/lib/rancher/k3s/server/db"
	_, err := e.PCT.ExecScript(ctx, b.CTID, fmt.Sprintf(`set -eu
cd %[1]s
mkdir -p backups
for f in state.db state.db-wal state.db-shm; do [ -e "$f" ] && cp -a "$f" "backups/%[2]s-$f"; done
`, dir, name))
	if err != nil {
		return fmt.Errorf("SQLite backup: %w", err)
	}
	e.logf("SQLite datastore copied to %s/backups/%s-state.db on %s", dir, name, b.Hostname)
	return nil
}

// pushBinary replaces the k3s binary atomically (the running binary cannot
// be overwritten in place).
func (e *Engine) pushBinary(ctx context.Context, n plan.Node) error {
	art, err := e.ensureArtifact(ctx)
	if err != nil {
		return err
	}
	if err := e.PCT.Push(ctx, n.CTID, art.Path, k3s.BinaryPath+".pk3s-new", "0755"); err != nil {
		return err
	}
	_, err = e.PCT.Exec(ctx, n.CTID, "mv", "-f", k3s.BinaryPath+".pk3s-new", k3s.BinaryPath)
	return err
}

func (e *Engine) nodeStatus(ctx context.Context, host string) (k3s.NodeStatus, bool) {
	nodes, err := e.NodeStatuses(ctx)
	if err != nil {
		return k3s.NodeStatus{}, false
	}
	for _, ns := range nodes {
		if ns.Name == host {
			return ns, true
		}
	}
	return k3s.NodeStatus{}, false
}

func (e *Engine) upgradeNode(ctx context.Context, n plan.Node) error {
	if ns, ok := e.nodeStatus(ctx, n.Hostname); ok && ns.Ready && ns.Version == e.Plan.K3sVersion {
		e.logf("%s already runs %s", n.Hostname, ns.Version)
		_, _ = e.kubectl(ctx, "uncordon", n.Hostname)
		return nil
	}
	if n.Role == k3s.RoleAgent {
		e.logf("%s: draining (timeout %s)", n.Hostname, DrainTimeout)
		if _, err := e.kubectl(ctx, "drain", n.Hostname, "--ignore-daemonsets", "--delete-emptydir-data",
			"--timeout="+DrainTimeout.String()); err != nil {
			return fmt.Errorf("drain %s: %w (check PodDisruptionBudgets, then resume)", n.Hostname, err)
		}
	} else if _, err := e.kubectl(ctx, "cordon", n.Hostname); err != nil {
		return fmt.Errorf("cordon %s: %w", n.Hostname, err)
	}
	if err := e.pushBinary(ctx, n); err != nil {
		return err
	}
	if _, err := e.PCT.Exec(ctx, n.CTID, "systemctl", "restart", "--no-block", n.Role.ServiceName()); err != nil {
		return err
	}
	deadline := time.Now().Add(e.Timeouts.ServerReady)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ns, ok := e.nodeStatus(ctx, n.Hostname)
		if ok && ns.Ready && ns.Version == e.Plan.K3sVersion {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not report Ready with %s after %s; last log lines:\n%s",
				n.Hostname, e.Plan.K3sVersion, e.Timeouts.ServerReady, e.journalTail(ctx, n))
		}
		e.sleep(e.Timeouts.Poll)
	}
	if _, err := e.kubectl(ctx, "uncordon", n.Hostname); err != nil {
		return fmt.Errorf("uncordon %s: %w", n.Hostname, err)
	}
	e.logf("%s upgraded to %s", n.Hostname, e.Plan.K3sVersion)
	return nil
}

func (e *Engine) removeNode(ctx context.Context, n plan.Node) error {
	if _, err := e.PCT.Status(ctx, n.CTID); err == nil {
		owned, err := e.ownsCT(ctx, n)
		if err != nil {
			return err
		}
		if !owned {
			return fmt.Errorf("CT %d does not carry tag %s; refusing to remove it", n.CTID, plan.ClusterTag(e.Plan.Spec.Name))
		}
	}
	if _, ok := e.nodeStatus(ctx, n.Hostname); ok {
		e.logf("%s: draining (timeout %s)", n.Hostname, DrainTimeout)
		if _, err := e.kubectl(ctx, "drain", n.Hostname, "--ignore-daemonsets", "--delete-emptydir-data",
			"--timeout="+DrainTimeout.String()); err != nil {
			return fmt.Errorf("drain %s: %w (check PodDisruptionBudgets, then resume)", n.Hostname, err)
		}
		if _, err := e.kubectl(ctx, "delete", "node", n.Hostname, "--ignore-not-found"); err != nil {
			return err
		}
	}
	if _, err := e.PCT.Status(ctx, n.CTID); err == nil {
		if err := e.PCT.Stop(ctx, n.CTID); err != nil {
			return err
		}
		if err := e.PCT.Destroy(ctx, n.CTID); err != nil {
			return err
		}
	}
	e.State.RemoveResource(state.KindContainer, strconv.Itoa(n.CTID))
	if n.DataDir != "" {
		e.logf("%s removed; data directory %s kept on the host", n.Hostname, n.DataDir)
	} else {
		e.logf("%s removed", n.Hostname)
	}
	return e.Store.Save(e.State)
}
