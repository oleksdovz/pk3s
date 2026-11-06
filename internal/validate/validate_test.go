package validate_test

import (
	"strings"
	"testing"

	"github.com/oleksdovz/pk3s/internal/plan"
	"github.com/oleksdovz/pk3s/internal/spec"
	"github.com/oleksdovz/pk3s/internal/testutil"
	"github.com/oleksdovz/pk3s/internal/validate"
)

func good() spec.ClusterSpec {
	s := spec.Default()
	s.Name = "lab"
	s.Servers, s.Agents = 1, 2
	s.Server.MemoryMiB = 4096
	s.Template = spec.Template{Storage: "local", Name: "debian-13-standard_13.1-2_amd64.tar.zst"}
	s.RootfsStorage = "x-data"
	s.Network = spec.Network{Bridge: "vmbr0", FirstIP: "192.168.0.51", Prefix: 24, Gateway: "192.168.0.1", DNS: []string{"192.168.0.2"}}
	s.Security.AcknowledgePrivilegedRisk = true
	return s
}

func all(t *testing.T, s spec.ClusterSpec, probes validate.Probes) validate.Findings {
	t.Helper()
	inv := testutil.HPInventory(t)
	f := validate.Spec(s, inv)
	p, err := plan.Build(s, inv, "v1.37.2+k3s1")
	if err != nil {
		return append(f, validate.Finding{Severity: validate.Error, Field: "build", Message: err.Error()})
	}
	return append(f, validate.Plan(p, inv, probes)...)
}

func hasError(fs validate.Findings, field, substr string) bool {
	for _, f := range fs {
		if f.Severity == validate.Error && strings.HasPrefix(f.Field, field) && strings.Contains(f.Message, substr) {
			return true
		}
	}
	return false
}

func TestGoodSpecHasNoErrors(t *testing.T) {
	fs := all(t, good(), validate.Probes{})
	if fs.HasErrors() {
		t.Fatalf("unexpected errors:\n%v", fs)
	}
}

func TestErrors(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*spec.ClusterSpec)
		probes validate.Probes
		field  string
		substr string
	}{
		{"two servers", func(s *spec.ClusterSpec) { s.Servers = 2 }, validate.Probes{}, "servers", "quorum"},
		{"four servers", func(s *spec.ClusterSpec) { s.Servers = 4 }, validate.Probes{}, "servers", "odd"},
		{"server ram", func(s *spec.ClusterSpec) { s.Server.MemoryMiB = 1024 }, validate.Probes{}, "server_resources.memory_mib", "minimum"},
		{"agent ram", func(s *spec.ClusterSpec) { s.Agent.MemoryMiB = 256 }, validate.Probes{}, "agent_resources.memory_mib", "minimum"},
		{"risk not acknowledged", func(s *spec.ClusterSpec) { s.Security.AcknowledgePrivilegedRisk = false }, validate.Probes{}, "security.acknowledge", "host root"},
		{"bad name", func(s *spec.ClusterSpec) { s.Name = "Lab_1" }, validate.Probes{}, "name", "lowercase"},
		{"bad prefix", func(s *spec.ClusterSpec) { s.HostnamePrefix = "-x" }, validate.Probes{}, "hostname_prefix", "lowercase"},
		{"ctid conflict with existing CT 9000", func(s *spec.ClusterSpec) { s.CTIDStart = 8999 }, validate.Probes{}, "nodes.node1", "CT ID 9000"},
		{"ip of existing CT", func(s *spec.ClusterSpec) { s.Network.FirstIP = "192.168.0.6" }, validate.Probes{}, "nodes.main1", "lxc 9000"},
		{"ip of host bridge", func(s *spec.ClusterSpec) { s.Network.FirstIP = "192.168.0.5" }, validate.Probes{}, "nodes.main1", "host bridge"},
		{"ip answers ping", func(s *spec.ClusterSpec) {}, validate.Probes{IPInUse: func(ip string) bool { return ip == "192.168.0.52" }}, "nodes.node1", "answers"},
		{"gateway outside subnet", func(s *spec.ClusterSpec) { s.Network.Gateway = "10.0.0.1" }, validate.Probes{}, "nodes.main1", "not in"},
		{"runs past subnet", func(s *spec.ClusterSpec) { s.Network.FirstIP = "192.168.0.254" }, validate.Probes{}, "nodes.node1", "broadcast"},
		{"missing storage", func(s *spec.ClusterSpec) { s.RootfsStorage = "nope" }, validate.Probes{}, "rootfs_storage", "not found"},
		{"storage without rootdir", func(s *spec.ClusterSpec) { s.RootfsStorage = "local" }, validate.Probes{}, "rootfs_storage", "rootdir"},
		{"template missing", func(s *spec.ClusterSpec) { s.Template.Name = "debian-13-standard_99_amd64.tar.zst" }, validate.Probes{}, "template.name", "not found"},
		{"alpine template", func(s *spec.ClusterSpec) {
			s.Template = spec.Template{Storage: "DATA", Name: "alpine-3.23-default_20260116_amd64.tar.xz"}
		}, validate.Probes{}, "template.name", "Debian/Ubuntu"},
		{"bridge missing", func(s *spec.ClusterSpec) { s.Network.Bridge = "vmbr9" }, validate.Probes{}, "network.bridge", "not found"},
		{"disk too big", func(s *spec.ClusterSpec) { s.Agent.DiskGiB = 2000 }, validate.Probes{}, "rootfs_storage", "available"},
		{"package injection", func(s *spec.ClusterSpec) { s.OS.ExtraPackages = []string{"vim; rm -rf /"} }, validate.Probes{}, "os.extra_packages", "invalid"},
		{"persistent system path", func(s *spec.ClusterSpec) { s.Persistent = spec.PersistentData{Enabled: true, HostPath: "/etc/k3s"} }, validate.Probes{}, "persistent_data.host_path", "system path"},
		{"non-empty data dir", func(s *spec.ClusterSpec) {
			s.Persistent = spec.PersistentData{Enabled: true, HostPath: "/data/k3s-storage"}
		},
			validate.Probes{DirNonEmpty: func(p string) bool { return strings.HasSuffix(p, "lab-main1") }}, "nodes.main1", "not empty"},
		{"duplicate per-node IP", func(s *spec.ClusterSpec) {
			s.NodeNetwork = []spec.NodeNetwork{{Node: "node1", IP: "192.168.0.60"}, {Node: "node2", IP: "192.168.0.60"}}
		}, validate.Probes{}, "nodes.node2", "assigned to both"},
		{"bad per-node DNS", func(s *spec.ClusterSpec) {
			s.NodeNetwork = []spec.NodeNetwork{{Node: "node1", DNS: []string{"dns.example"}}}
		}, validate.Probes{}, "nodes.node1", "DNS"},
		{"pod CIDR overlaps LAN", func(s *spec.ClusterSpec) { s.K3s.ClusterCIDR = "192.168.0.0/16" }, validate.Probes{}, "k3s", "overlaps"},
		{"negative delay", func(s *spec.ClusterSpec) { s.Startup.NodeDelaySec = -1 }, validate.Probes{}, "startup", "negative"},
		{"state exists", func(s *spec.ClusterSpec) {}, validate.Probes{StateExists: func(string) bool { return true }}, "name", "already exists"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := good()
			c.mutate(&s)
			fs := all(t, s, c.probes)
			if !hasError(fs, c.field, c.substr) {
				t.Fatalf("expected error on %s containing %q, got:\n%v", c.field, c.substr, fs)
			}
		})
	}
}

func TestWarnings(t *testing.T) {
	s := good()
	s.Server.MemoryMiB = 2048
	s.Security.Fuse = true
	s.Network.VLAN = 30 // tagged: subnets are not tied to the bridge address
	s.NodeNetwork = []spec.NodeNetwork{{Node: "node2", IP: "10.20.0.5", Prefix: 24, Gateway: "10.20.0.1"}}
	fs := all(t, s, validate.Probes{})
	if fs.HasErrors() {
		t.Fatalf("unexpected errors: %v", fs)
	}
	for _, field := range []string{"server_resources.memory_mib", "security.fuse", "node_network"} {
		found := false
		for _, f := range fs {
			if f.Severity == validate.Warning && f.Field == field {
				found = true
			}
		}
		if !found {
			t.Errorf("expected warning on %s; got %v", field, fs)
		}
	}
}

func TestUnprivilegedNFSWarning(t *testing.T) {
	s := good()
	s.Security.Profile = spec.ProfileUnprivileged
	fs := validate.Spec(s, testutil.HPInventory(t))
	if len(fs.ForField("os.nfs_client")) == 0 || len(fs.ForField("security.profile")) == 0 {
		t.Fatalf("expected experimental + NFS warnings: %v", fs)
	}
}

func TestBridgeSubnetMismatch(t *testing.T) {
	s := good()
	s.Network.Bridge = "vmbr100" // 192.168.100.1/24 on the fixture host
	fs := all(t, s, validate.Probes{})
	if !hasError(fs, "nodes.main1", "not in the subnet of bridge vmbr100") {
		t.Fatalf("expected bridge/subnet error: %v", fs)
	}
	s.Network.VLAN = 30 // a tagged VLAN may use any subnet
	if hasError(all(t, s, validate.Probes{}), "nodes.main1", "subnet of bridge") {
		t.Fatal("VLAN-tagged nodes must not be checked against the bridge subnet")
	}
}
