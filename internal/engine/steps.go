package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/oleksdovz/pk3s/internal/k3s"
	"github.com/oleksdovz/pk3s/internal/lxcprofile"
	"github.com/oleksdovz/pk3s/internal/plan"
	"github.com/oleksdovz/pk3s/internal/pve"
	"github.com/oleksdovz/pk3s/internal/runner"
	"github.com/oleksdovz/pk3s/internal/state"
)

func (e *Engine) hostModules(ctx context.Context) error {
	content := "# Managed by pk3s: kernel modules required by k3s in LXC containers.\n" +
		strings.Join(lxcprofile.ModulesFor(e.Plan.Spec.OS.NFSClient), "\n") + "\n"
	if err := e.FS.WriteFile(lxcprofile.ModulesLoadFile, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", lxcprofile.ModulesLoadFile, err)
	}
	if err := e.addResource(state.KindHostFile, lxcprofile.ModulesLoadFile); err != nil {
		return err
	}
	args := append([]string{"-a"}, lxcprofile.ModulesFor(e.Plan.Spec.OS.NFSClient)...)
	if _, err := e.R.Run(ctx, runner.Cmd{Name: "modprobe", Args: args}); err != nil {
		return fmt.Errorf("load kernel modules: %w", err)
	}
	e.logf("kernel modules loaded and persisted in %s", lxcprofile.ModulesLoadFile)
	return nil
}

func (e *Engine) template(ctx context.Context) error {
	t := e.Plan.Spec.Template
	e.logf("downloading template %s to %s", t.Name, t.Storage)
	if err := e.Pveam.Download(ctx, t.Storage, t.Name); err != nil {
		return err
	}
	return e.addResource(state.KindTemplate, t.VolID())
}

func ctID(n plan.Node) string { return fmt.Sprint(n.CTID) }

// ownsCT checks that an existing container belongs to this cluster.
func (e *Engine) ownsCT(ctx context.Context, n plan.Node) (bool, error) {
	conf, err := e.PCT.ReadConfig(ctx, n.CTID)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(conf, "\n") {
		if tags, ok := strings.CutPrefix(line, "tags:"); ok {
			for _, t := range pve.SplitTags(strings.TrimSpace(tags)) {
				if t == plan.ClusterTag(e.Plan.Spec.Name) {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func (e *Engine) createCT(ctx context.Context, n plan.Node) error {
	if _, err := e.PCT.Status(ctx, n.CTID); err == nil {
		// The CT exists. Only continue if pk3s created it for this cluster.
		if e.State.HasResource(state.KindContainer, ctID(n)) {
			e.logf("CT %d already created by pk3s, continuing", n.CTID)
			return nil
		}
		owned, err := e.ownsCT(ctx, n)
		if err != nil {
			return err
		}
		if !owned {
			return fmt.Errorf("CT %d exists and was not created by pk3s for cluster %q; refusing to touch it", n.CTID, e.Plan.Spec.Name)
		}
		e.logf("CT %d carries tag %s; adopting it into the ledger", n.CTID, plan.ClusterTag(e.Plan.Spec.Name))
		return e.addResource(state.KindContainer, ctID(n))
	}
	if err := e.PCT.Create(ctx, e.Plan.CreateOpts(n)); err != nil {
		return err
	}
	return e.addResource(state.KindContainer, ctID(n))
}

func (e *Engine) configureCT(ctx context.Context, n plan.Node) error {
	if err := e.PCT.Stop(ctx, n.CTID); err != nil {
		return err
	}
	if err := e.PCT.AppendConfig(ctx, n.CTID, e.Plan.Profile.Lines()); err != nil {
		return err
	}
	if n.DataDir == "" {
		return nil
	}
	conf, err := e.PCT.ReadConfig(ctx, n.CTID)
	if err != nil {
		return err
	}
	if strings.Contains(conf, "\nmp0:") || strings.HasPrefix(conf, "mp0:") {
		return nil
	}
	if err := e.FS.MkdirAll(n.DataDir, 0o711); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	if err := e.addResource(state.KindDataDir, n.DataDir); err != nil {
		return err
	}
	return e.PCT.SetMountPoint(ctx, n.CTID, 0, n.DataDir, k3s.LocalStoragePath)
}

func (e *Engine) startCT(ctx context.Context, n plan.Node) error {
	if err := e.PCT.Start(ctx, n.CTID); err != nil {
		return err
	}
	return e.PCT.WaitReady(ctx, n.CTID, e.Timeouts.CTReady)
}

// PrepareOpts selects optional packages for node preparation.
type PrepareOpts struct {
	SSH   bool
	NFS   bool
	Extra []string // validated package names
}

// Packages returns the apt packages to install.
func (o PrepareOpts) Packages() []string {
	pkgs := []string{"ca-certificates", "iptables", "nftables", "conntrack", "ebtables", "ethtool", "socat", "iproute2", "kmod"}
	if o.SSH {
		pkgs = append(pkgs, "openssh-server")
	}
	if o.NFS {
		pkgs = append(pkgs, "nfs-common")
	}
	return append(pkgs, o.Extra...)
}

// PrepareScript returns the OS preparation script run inside a container.
// It is idempotent and fails loudly when a hard requirement is missing.
func PrepareScript(o PrepareOpts) string {
	pkgs := strings.Join(o.Packages(), " ")
	return `set -eu
export DEBIAN_FRONTEND=noninteractive
apt-get update -q
apt-get install -y -q --no-install-recommends ` + pkgs + `
mkdir -p /etc/sysctl.d
cat > /etc/sysctl.d/90-pk3s.conf <<'EOF'
# Managed by pk3s
net.ipv4.ip_forward = 1
net.bridge.bridge-nf-call-iptables = 1
net.bridge.bridge-nf-call-ip6tables = 1
EOF
sysctl -p /etc/sysctl.d/90-pk3s.conf >/dev/null 2>&1 || echo "WARN: some sysctls could not be applied inside the container"
[ "$(sysctl -n net.ipv4.ip_forward)" = 1 ] || { echo "ERROR: net.ipv4.ip_forward is not enabled" >&2; exit 1; }
[ "$(sysctl -n net.bridge.bridge-nf-call-iptables 2>/dev/null || echo 0)" = 1 ] || echo "WARN: bridge-nf-call-iptables is not 1 (is br_netfilter loaded on the host?)"
[ -e /dev/kmsg ] || { echo "ERROR: /dev/kmsg is missing; kubelet will not start" >&2; exit 1; }
[ "$(stat -fc %T /sys/fs/cgroup)" = cgroup2fs ] || { echo "ERROR: cgroup v2 is not mounted in the container" >&2; exit 1; }
if [ "$(tail -n +2 /proc/swaps | wc -l)" -gt 0 ]; then echo "WARN: swap is visible in the container"; fi
mkdir -p -m 0700 /etc/rancher/k3s
echo "pk3s: node prepared"
`
}

func (e *Engine) prepareNode(ctx context.Context, n plan.Node) error {
	s := e.Plan.Spec
	out, err := e.PCT.ExecScript(ctx, n.CTID, PrepareScript(PrepareOpts{
		SSH: s.Access.SSHPublicKeyFile != "", NFS: s.OS.NFSClient, Extra: s.OS.ExtraPackages,
	}))
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "WARN:") || strings.HasPrefix(line, "ERROR:") {
			e.logf("%s: %s", n.Hostname, line)
		}
	}
	return err
}

func (e *Engine) ensureTokens() error {
	if e.Secrets.ServerToken != "" && e.Secrets.AgentToken != "" {
		return nil
	}
	var err error
	if e.Secrets.ServerToken == "" {
		if e.Secrets.ServerToken, err = k3s.GenerateToken(); err != nil {
			return err
		}
	}
	if e.Secrets.AgentToken == "" {
		if e.Secrets.AgentToken, err = k3s.GenerateToken(); err != nil {
			return err
		}
	}
	if e.Redactor != nil {
		e.Redactor.Add(e.Secrets.ServerToken)
		e.Redactor.Add(e.Secrets.AgentToken)
	}
	// Persist before use so a resumed run uses the same tokens.
	return e.Store.SaveSecrets(e.Secrets)
}

func (e *Engine) installK3s(ctx context.Context, n plan.Node) error {
	art, err := e.ensureArtifact(ctx)
	if err != nil {
		return err
	}
	if err := e.ensureTokens(); err != nil {
		return err
	}
	join := e.Secrets.SecureServerToken
	if n.Role == k3s.RoleAgent {
		join = e.Secrets.SecureAgentToken
	}
	if !n.Bootstrap && join == "" {
		return fmt.Errorf("join token not available; the bootstrap server must be installed first")
	}

	if err := e.PCT.Push(ctx, n.CTID, art.Path, k3s.BinaryPath, "0755"); err != nil {
		return err
	}
	if _, err := e.PCT.ExecScript(ctx, n.CTID, `set -eu
for l in kubectl crictl ctr; do ln -sf k3s /usr/local/bin/$l; done
mkdir -p -m 0700 /etc/rancher/k3s
`); err != nil {
		return err
	}

	unitPath, cleanUnit, err := e.tmpFile(n.Hostname+".service", []byte(k3s.SystemdUnit(n.Role)))
	if err != nil {
		return err
	}
	defer cleanUnit()
	if err := e.PCT.Push(ctx, n.CTID, unitPath, "/etc/systemd/system/"+n.Role.ServiceName()+".service", "0644"); err != nil {
		return err
	}

	cfg, err := e.Plan.NodeConfig(n, e.Secrets.ServerToken, e.Secrets.AgentToken, join).Render()
	if err != nil {
		return err
	}
	cfgPath, cleanCfg, err := e.tmpFile(n.Hostname+".config.yaml", cfg)
	if err != nil {
		return err
	}
	defer cleanCfg()
	if err := e.PCT.Push(ctx, n.CTID, cfgPath, k3s.ConfigPath, "0600"); err != nil {
		return err
	}

	svc := n.Role.ServiceName()
	// --no-block: Type=notify with TimeoutStartSec=0 would otherwise block
	// forever on a broken node; readiness is checked by the next step.
	_, err = e.PCT.ExecScript(ctx, n.CTID, fmt.Sprintf(`set -eu
systemctl daemon-reload
systemctl enable %[1]s >/dev/null 2>&1
systemctl restart --no-block %[1]s
`, svc))
	return err
}

func (e *Engine) journalTail(ctx context.Context, n plan.Node) string {
	out, _ := e.PCT.Exec(ctx, n.CTID, "journalctl", "-u", n.Role.ServiceName(), "-n", "25", "--no-pager", "-o", "cat")
	return e.redact(out)
}

func (e *Engine) waitNode(ctx context.Context, n plan.Node) error {
	timeout := e.Timeouts.AgentReady
	if n.Role == k3s.RoleServer {
		timeout = e.Timeouts.ServerReady
	}
	deadline := time.Now().Add(timeout)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		active, _ := e.PCT.Exec(ctx, n.CTID, "systemctl", "is-active", n.Role.ServiceName())
		if active == "failed" {
			return fmt.Errorf("%s failed on %s; last log lines:\n%s", n.Role.ServiceName(), n.Hostname, e.journalTail(ctx, n))
		}
		if active == "active" {
			if n.Role == k3s.RoleServer {
				out, err := e.PCT.Exec(ctx, n.CTID, k3s.BinaryPath, "kubectl", "get", "--raw", "/readyz")
				if err == nil && out == "ok" {
					e.logf("%s: API server ready", n.Hostname)
					return nil
				}
			} else {
				_, err := e.PCT.Exec(ctx, e.bootstrap().CTID, k3s.BinaryPath, "kubectl", "get", "node", n.Hostname, "-o", "name")
				if err == nil {
					e.logf("%s: registered with the cluster", n.Hostname)
					return nil
				}
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s on %s not ready after %s (state %q); last log lines:\n%s",
				n.Role.ServiceName(), n.Hostname, timeout, active, e.journalTail(ctx, n))
		}
		e.sleep(e.Timeouts.Poll)
	}
}

func (e *Engine) fetchTokens(ctx context.Context) error {
	b := e.bootstrap()
	srv, err := e.PCT.ExecSecret(ctx, b.CTID, "cat", k3s.ServerTokenPath)
	if err != nil {
		return fmt.Errorf("read server token: %w", err)
	}
	agt, err := e.PCT.ExecSecret(ctx, b.CTID, "cat", k3s.AgentTokenPath)
	if err != nil {
		return fmt.Errorf("read agent token: %w", err)
	}
	if !strings.HasPrefix(srv, "K10") || !strings.HasPrefix(agt, "K10") {
		return fmt.Errorf("unexpected token format (expected secure K10 tokens)")
	}
	e.Secrets.SecureServerToken, e.Secrets.SecureAgentToken = srv, agt
	if e.Redactor != nil {
		e.Redactor.Add(srv)
		e.Redactor.Add(agt)
	}
	if err := e.Store.SaveSecrets(e.Secrets); err != nil {
		return err
	}
	e.logf("join tokens stored in %s (mode 0600)", e.Store.Path("secrets.json"))
	return nil
}

// NodeStatuses returns the nodes as reported by the bootstrap server.
func (e *Engine) NodeStatuses(ctx context.Context) ([]k3s.NodeStatus, error) {
	out, err := e.PCT.Exec(ctx, e.bootstrap().CTID, k3s.BinaryPath, "kubectl", "get", "nodes", "-o", "json")
	if err != nil {
		return nil, err
	}
	return k3s.ParseNodes([]byte(out))
}

func (e *Engine) clusterReady(ctx context.Context) error {
	deadline := time.Now().Add(e.Timeouts.ClusterReady)
	want := map[string]bool{}
	for _, n := range e.Plan.Nodes {
		want[n.Hostname] = true
	}
	last := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		nodes, err := e.NodeStatuses(ctx)
		if err == nil {
			ready := 0
			var notReady []string
			seen := map[string]bool{}
			for _, ns := range nodes {
				seen[ns.Name] = true
				if want[ns.Name] && ns.Ready {
					ready++
				} else if want[ns.Name] {
					notReady = append(notReady, ns.Name)
				}
			}
			for h := range want {
				if !seen[h] {
					notReady = append(notReady, h+" (not registered)")
				}
			}
			msg := fmt.Sprintf("%d/%d nodes Ready", ready, len(want))
			if msg != last {
				e.logf("%s", msg)
				last = msg
			}
			if ready == len(want) {
				return nil
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("timed out after %s; not ready: %s", e.Timeouts.ClusterReady, strings.Join(notReady, ", "))
			}
		} else if time.Now().After(deadline) {
			return fmt.Errorf("timed out querying nodes: %w", err)
		}
		e.sleep(e.Timeouts.Poll)
	}
}

func (e *Engine) kubeconfig(ctx context.Context) error {
	kc, err := e.PCT.ExecSecret(ctx, e.bootstrap().CTID, "cat", k3s.KubeconfigPath)
	if err != nil {
		return fmt.Errorf("read kubeconfig: %w", err)
	}
	path, err := e.Store.WriteFile("kubeconfig", k3s.RewriteKubeconfig([]byte(kc+"\n"), e.Plan.Endpoint, e.Plan.Spec.Name))
	if err != nil {
		return err
	}
	e.logf("kubeconfig written to %s (mode 0600)", path)
	return nil
}
