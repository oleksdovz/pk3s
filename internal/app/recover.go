package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/oleksdovz/pk3s/internal/k3s"
	"github.com/oleksdovz/pk3s/internal/plan"
	"github.com/oleksdovz/pk3s/internal/pve"
	"github.com/oleksdovz/pk3s/internal/spec"
	"github.com/oleksdovz/pk3s/internal/state"
)

// findMain1 returns the running main1 container of a cluster, found by the
// pk3s-<name> Proxmox tag.
func findMain1(inv *pve.Inventory, name string) (*pve.Guest, bool) {
	for i := range inv.Guests {
		g := &inv.Guests[i]
		if g.Type == "lxc" && g.HasTag(plan.ClusterTag(name)) && strings.HasSuffix(g.Name, "-main1") {
			return g, true
		}
	}
	return nil, false
}

// recoverState rebuilds the host-side state of a cluster from the
// ConfigMap kube-system/pk3s-state, read through kubectl on main1. It
// returns false when no container of the cluster exists on this host.
// Tokens are re-read from main1 and the kubeconfig is exported again.
func (a *App) recoverState(ctx context.Context, name string, inv *pve.Inventory) (bool, error) {
	main, ok := findMain1(inv, name)
	if !ok {
		return false, nil
	}
	if main.Status != "running" {
		return false, fmt.Errorf("found cluster %q (CT %d %s) but it is %s; start it so pk3s can read its state", name, main.VMID, main.Name, main.Status)
	}
	pct := pve.PCT{R: a.R}
	out, err := pct.Exec(ctx, main.VMID, k3s.BinaryPath, "kubectl", "get", "configmap", k3s.StateConfigMapName,
		"-n", k3s.StateConfigMapNamespace, "-o", "json")
	if err != nil {
		return false, fmt.Errorf("cluster %q exists (CT %d) but its state ConfigMap %s/%s could not be read: %w",
			name, main.VMID, k3s.StateConfigMapNamespace, k3s.StateConfigMapName, err)
	}
	cs, err := k3s.ParseStateConfigMap([]byte(out))
	if err != nil {
		return false, err
	}
	s, err := spec.Parse([]byte(cs.SpecYAML))
	if err != nil {
		return false, err
	}
	if s.Name != name {
		return false, fmt.Errorf("state ConfigMap on CT %d belongs to cluster %q, not %q", main.VMID, s.Name, name)
	}
	var layout []plan.NodeRecord
	if err := json.Unmarshal([]byte(cs.LayoutJSON), &layout); err != nil {
		return false, fmt.Errorf("decode layout: %w", err)
	}

	// Tokens: raw ones from the config pk3s wrote on main1, secure ones
	// from the k3s token files. Never logged.
	var cfg struct {
		Token      string `yaml:"token"`
		AgentToken string `yaml:"agent-token"`
	}
	raw, err := pct.ExecSecret(ctx, main.VMID, "cat", k3s.ConfigPath)
	if err != nil {
		return false, fmt.Errorf("read %s on CT %d: %w", k3s.ConfigPath, main.VMID, err)
	}
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		return false, fmt.Errorf("parse %s on CT %d: %w", k3s.ConfigPath, main.VMID, err)
	}
	sec := &state.Secrets{ServerToken: cfg.Token, AgentToken: cfg.AgentToken}
	if sec.SecureServerToken, err = pct.ExecSecret(ctx, main.VMID, "cat", k3s.ServerTokenPath); err != nil {
		return false, err
	}
	if sec.SecureAgentToken, err = pct.ExecSecret(ctx, main.VMID, "cat", k3s.AgentTokenPath); err != nil {
		return false, err
	}
	for _, t := range []string{sec.ServerToken, sec.AgentToken, sec.SecureServerToken, sec.SecureAgentToken} {
		a.Redactor.Add(t)
	}

	store := &state.Store{Root: a.Root, Cluster: name}
	if err := store.Init(); err != nil {
		return false, err
	}
	if err := store.Lock(); err != nil {
		return false, err
	}
	defer store.Unlock()
	st := state.New(name, cs.K3sVersion)
	st.Layout = layout
	for _, r := range layout {
		if g, ok := inv.Guest(r.CTID); ok && g.HasTag(plan.ClusterTag(name)) {
			st.AddResource(state.Resource{Kind: state.KindContainer, ID: strconv.Itoa(r.CTID), Node: inv.Node})
		}
		if r.DataDir != "" {
			st.AddResource(state.Resource{Kind: state.KindDataDir, ID: r.DataDir, Node: inv.Node})
		}
	}
	if err := spec.Save(store.Path("spec.yaml"), s); err != nil {
		return false, err
	}
	if err := store.SaveSecrets(sec); err != nil {
		return false, err
	}
	if kc, err := pct.ExecSecret(ctx, main.VMID, "cat", k3s.KubeconfigPath); err == nil && len(layout) > 0 {
		_, _ = store.WriteFile("kubeconfig", k3s.RewriteKubeconfig([]byte(kc+"\n"), layout[0].IP, name))
	}
	if err := store.Save(st); err != nil {
		return false, err
	}
	return true, nil
}
