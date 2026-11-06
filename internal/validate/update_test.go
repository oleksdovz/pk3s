package validate_test

import (
	"strings"
	"testing"

	"github.com/oleksdovz/pk3s/internal/plan"
	"github.com/oleksdovz/pk3s/internal/spec"
	"github.com/oleksdovz/pk3s/internal/testutil"
	"github.com/oleksdovz/pk3s/internal/validate"
)

func updateFindings(t *testing.T, mutate func(*spec.ClusterSpec), from, to string) validate.Findings {
	t.Helper()
	cur := good() // 1 main + 2 node
	p, err := plan.Build(cur, testutil.HPInventory(t), from)
	if err != nil {
		t.Fatal(err)
	}
	tgt := cur
	mutate(&tgt)
	return validate.Update(plan.UpdateInput{Current: cur, Layout: p.Records(), FromVersion: from, Target: tgt, ToVersion: to})
}

func TestUpdateRules(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(*spec.ClusterSpec)
		from, to string
		field    string
		substr   string
	}{
		{"downgrade", func(*spec.ClusterSpec) {}, "v1.37.2+k3s1", "v1.37.1+k3s1", "k3s.version", "downgrade"},
		{"skip minor", func(*spec.ClusterSpec) {}, "v1.35.10+k3s1", "v1.37.2+k3s1", "k3s.version", "one minor"},
		{"remove servers", func(s *spec.ClusterSpec) { s.Servers = 0 }, "v1.37.2+k3s1", "v1.37.2+k3s1", "servers", "not supported"},
		{"security change", func(s *spec.ClusterSpec) { s.Security.TunDevice = true }, "v1.37.2+k3s1", "v1.37.2+k3s1", "security", "cannot be changed"},
		{"prefix change", func(s *spec.ClusterSpec) { s.HostnamePrefix = "other" }, "v1.37.2+k3s1", "v1.37.2+k3s1", "hostname_prefix", "cannot"},
		{"disk shrink", func(s *spec.ClusterSpec) { s.Agent.DiskGiB = 20 }, "v1.37.2+k3s1", "v1.37.2+k3s1", "disk_gib", "only grow"},
		{"existing node network", func(s *spec.ClusterSpec) {
			s.NodeNetwork = []spec.NodeNetwork{{Node: "node1", IP: "192.168.0.99"}}
		}, "v1.37.2+k3s1", "v1.37.2+k3s1", "node_network", "cannot be changed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fs := updateFindings(t, c.mutate, c.from, c.to)
			if !hasError(fs, c.field, c.substr) {
				t.Fatalf("expected error on %s containing %q:\n%v", c.field, c.substr, fs)
			}
		})
	}
}

func TestUpdateAllowed(t *testing.T) {
	fs := updateFindings(t, func(s *spec.ClusterSpec) {
		s.Agents = 4
		s.Agent.MemoryMiB = 4096
		s.Agent.DiskGiB = 50
		s.K3s.Version = "v1.38.0+k3s1"
	}, "v1.37.2+k3s1", "v1.38.0+k3s1")
	if fs.HasErrors() {
		t.Fatalf("unexpected errors: %v", fs)
	}
	// 1 -> 3 servers is a plain etcd join now (no SQLite anywhere).
	fs = updateFindings(t, func(s *spec.ClusterSpec) { s.Servers = 3 }, "v1.37.2+k3s1", "v1.37.2+k3s1")
	if fs.HasErrors() {
		t.Fatalf("1 -> 3 servers should be allowed: %v", fs)
	}
	fs = updateFindings(t, func(s *spec.ClusterSpec) { s.Agents = 1 }, "v1.37.2+k3s1", "v1.37.2+k3s1")
	if fs.HasErrors() || !strings.Contains(fs.String(), "removing 1 agent") {
		t.Fatalf("scale-down should warn, not fail: %v", fs)
	}
}
