package plan_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/oleksdovz/pk3s/internal/k3s"
	"github.com/oleksdovz/pk3s/internal/plan"
	"github.com/oleksdovz/pk3s/internal/spec"
	"github.com/oleksdovz/pk3s/internal/testutil"
)

const ver = "v1.37.2+k3s1"

func baseSpec() spec.ClusterSpec {
	s := spec.Default()
	s.Name = "lab"
	s.Servers, s.Agents = 3, 2
	s.Template = spec.Template{Storage: "local", Name: "debian-13-standard_13.1-2_amd64.tar.zst"}
	s.RootfsStorage = "x-data"
	s.Network = spec.Network{Bridge: "vmbr0", FirstIP: "192.168.0.51", Prefix: 24, Gateway: "192.168.0.1", DNS: []string{"192.168.0.2"}}
	s.Security.AcknowledgePrivilegedRisk = true
	return s
}

func TestLayout(t *testing.T) {
	s := baseSpec()
	s.HostnamePrefix = "k3s-lab"
	p, err := plan.Build(s, testutil.HPInventory(t), ver)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Nodes) != 5 {
		t.Fatalf("nodes: %d", len(p.Nodes))
	}
	want := []struct {
		host string
		id   int
		ip   string
		role k3s.Role
	}{
		{"k3s-lab-main1", 5000, "192.168.0.51", k3s.RoleServer},
		{"k3s-lab-main2", 5001, "192.168.0.52", k3s.RoleServer},
		{"k3s-lab-main3", 5002, "192.168.0.53", k3s.RoleServer},
		{"k3s-lab-node1", 5003, "192.168.0.54", k3s.RoleAgent},
		{"k3s-lab-node2", 5004, "192.168.0.55", k3s.RoleAgent},
	}
	for i, w := range want {
		n := p.Nodes[i]
		if n.Hostname != w.host || n.CTID != w.id || n.IP != w.ip || n.Role != w.role {
			t.Errorf("node %d = %+v, want %+v", i, n, w)
		}
	}
	if !p.Nodes[0].Bootstrap || p.Nodes[1].Bootstrap {
		t.Error("only the first server bootstraps")
	}
	if p.Endpoint != "192.168.0.51" || !strings.Contains(p.Datastore, "quorum 2") {
		t.Errorf("endpoint %s datastore %s", p.Endpoint, p.Datastore)
	}
	if !slices.Equal(p.Tags, []string{"pk3s", "pk3s-lab"}) {
		t.Errorf("tags %v", p.Tags)
	}
}

func TestPerNodeNetworkOverride(t *testing.T) {
	s := baseSpec()
	s.NodeNetwork = []spec.NodeNetwork{{Node: "node2", IP: "10.10.0.20", Prefix: 16, Gateway: "10.10.0.1", DNS: []string{"1.1.1.1"}}}
	p, err := plan.Build(s, testutil.HPInventory(t), ver)
	if err != nil {
		t.Fatal(err)
	}
	a2 := p.Nodes[4]
	if a2.IP != "10.10.0.20" || a2.Prefix != 16 || a2.Gateway != "10.10.0.1" || a2.DNS[0] != "1.1.1.1" || !a2.Overridden {
		t.Fatalf("override not applied: %+v", a2)
	}
	// Other nodes keep consecutive defaults.
	if p.Nodes[3].IP != "192.168.0.54" || p.Nodes[3].Gateway != "192.168.0.1" {
		t.Fatalf("a1 changed: %+v", p.Nodes[3])
	}
	opts := strings.Join(p.CreateOpts(a2).Args(), " ")
	if !strings.Contains(opts, "ip=10.10.0.20/16,gw=10.10.0.1") || !strings.Contains(opts, "--nameserver 1.1.1.1") {
		t.Fatalf("create args ignore override: %s", opts)
	}
}

func TestStartupOrder(t *testing.T) {
	p, err := plan.Build(baseSpec(), testutil.HPInventory(t), ver)
	if err != nil {
		t.Fatal(err)
	}
	main := strings.Join(p.CreateOpts(p.Nodes[0]).Args(), " ")
	node := strings.Join(p.CreateOpts(p.Nodes[3]).Args(), " ")
	if !strings.Contains(main, "--onboot 1") || !strings.Contains(main, "--startup order=1,up=10") {
		t.Errorf("main startup: %s", main)
	}
	if !strings.Contains(node, "--startup order=2,up=10") {
		t.Errorf("node startup: %s", node)
	}
}

func TestUnknownOverrideKey(t *testing.T) {
	s := baseSpec()
	s.NodeNetwork = []spec.NodeNetwork{{Node: "node9", IP: "10.0.0.1"}}
	if _, err := plan.Build(s, testutil.HPInventory(t), ver); err == nil {
		t.Fatal("expected error for unknown node key")
	}
}

func TestNodeConfigsSeparateTokens(t *testing.T) {
	p, err := plan.Build(baseSpec(), testutil.HPInventory(t), ver)
	if err != nil {
		t.Fatal(err)
	}
	boot := p.NodeConfig(p.Nodes[0], "SRV", "AGT", "")
	if !boot.ClusterInit || boot.Token != "SRV" || boot.AgentToken != "AGT" || boot.Server != "" {
		t.Errorf("bootstrap config: %+v", boot)
	}
	join := p.NodeConfig(p.Nodes[1], "SRV", "AGT", "K10join")
	if join.ClusterInit || join.Server != "https://192.168.0.51:6443" || join.Token != "K10join" || join.AgentToken != "AGT" {
		t.Errorf("joining server config: %+v", join)
	}
	agent := p.NodeConfig(p.Nodes[3], "SRV", "AGT", "K10agent")
	if agent.Token != "K10agent" || agent.AgentToken != "" || len(agent.TLSSAN) != 0 {
		t.Errorf("agent config must only carry the agent join token: %+v", agent)
	}
	if !slices.Contains(boot.KubeProxyArg, "conntrack-max-per-core=0") {
		t.Error("kube-proxy conntrack arg missing")
	}
	if !slices.Contains(boot.TLSSAN, "192.168.0.52") {
		t.Errorf("SANs should include all server IPs: %v", boot.TLSSAN)
	}
}

func TestSingleServerUsesEmbeddedEtcd(t *testing.T) {
	s := baseSpec()
	s.Servers = 1
	p, err := plan.Build(s, testutil.HPInventory(t), ver)
	if err != nil {
		t.Fatal(err)
	}
	if !p.NodeConfig(p.Nodes[0], "a", "b", "").ClusterInit || !strings.Contains(p.Datastore, "embedded etcd, 1 server") {
		t.Errorf("single server must use embedded etcd: %s", p.Datastore)
	}
}

func TestStepsAndHostChanges(t *testing.T) {
	s := baseSpec()
	s.Persistent = spec.PersistentData{Enabled: true, HostPath: "/data/k3s-storage"}
	p, err := plan.Build(s, testutil.HPInventory(t), ver)
	if err != nil {
		t.Fatal(err)
	}
	if p.Nodes[0].DataDir != "/data/k3s-storage/lab/lab-main1" {
		t.Errorf("data dir %q", p.Nodes[0].DataDir)
	}
	// Template exists on host: no download step.
	ids := []string{}
	for _, st := range p.Steps {
		ids = append(ids, st.ID)
	}
	if slices.Contains(ids, "template") {
		t.Error("template step should be absent when the template exists")
	}
	tokens := slices.Index(ids, "k3s-tokens")
	s2 := slices.Index(ids, "ct-create/lab-main2")
	if tokens < 0 || s2 < tokens {
		t.Errorf("tokens must be read before the second node is created: %v", ids)
	}
	if ids[0] != "host-modules" || ids[len(ids)-1] != "publish-state" {
		t.Errorf("unexpected order: %v", ids)
	}
	if !slices.Contains(p.MissingModules, "vxlan") || !slices.Contains(p.MissingModules, "overlay") {
		t.Errorf("missing modules on fixture host: %v", p.MissingModules)
	}
}
