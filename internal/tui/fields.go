package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/oleksdovz/pk3s/internal/pve"
	"github.com/oleksdovz/pk3s/internal/spec"
)

type fieldKind int

const (
	kindText fieldKind = iota
	kindInt
	kindChoice // cycles through options; Enter allows typing a custom value if custom is set
	kindToggle
)

// field binds a form row to a spec value.
type field struct {
	label   string
	help    string
	kind    fieldKind
	options []string
	custom  bool
	get     func(s *spec.ClusterSpec) string
	set     func(s *spec.ClusterSpec, v string) error
}

// section is one wizard page.
type section struct {
	title string
	// prefixes select validation findings shown on this page.
	prefixes []string
	fields   []field
	intro    string
}

func atoi(v string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0, fmt.Errorf("enter a whole number")
	}
	return n, nil
}

func intField(label, help string, ptr func(s *spec.ClusterSpec) *int) field {
	return field{
		label: label, help: help, kind: kindInt,
		get: func(s *spec.ClusterSpec) string { return strconv.Itoa(*ptr(s)) },
		set: func(s *spec.ClusterSpec, v string) error {
			n, err := atoi(v)
			if err == nil {
				*ptr(s) = n
			}
			return err
		},
	}
}

func textField(label, help string, ptr func(s *spec.ClusterSpec) *string) field {
	return field{
		label: label, help: help, kind: kindText,
		get: func(s *spec.ClusterSpec) string { return *ptr(s) },
		set: func(s *spec.ClusterSpec, v string) error { *ptr(s) = strings.TrimSpace(v); return nil },
	}
}

func listField(label, help string, ptr func(s *spec.ClusterSpec) *[]string) field {
	return field{
		label: label, help: help + " (comma separated)", kind: kindText,
		get: func(s *spec.ClusterSpec) string { return strings.Join(*ptr(s), ", ") },
		set: func(s *spec.ClusterSpec, v string) error { *ptr(s) = splitList(v); return nil },
	}
}

func toggleField(label, help string, ptr func(s *spec.ClusterSpec) *bool) field {
	return field{
		label: label, help: help, kind: kindToggle,
		get: func(s *spec.ClusterSpec) string { return yesNo(*ptr(s)) },
		set: func(s *spec.ClusterSpec, v string) error { *ptr(s) = v == "yes"; return nil },
	}
}

func choiceField(label, help string, options []string, custom bool, ptr func(s *spec.ClusterSpec) *string) field {
	return field{
		label: label, help: help, kind: kindChoice, options: options, custom: custom,
		get: func(s *spec.ClusterSpec) string { return *ptr(s) },
		set: func(s *spec.ClusterSpec, v string) error { *ptr(s) = strings.TrimSpace(v); return nil },
	}
}

// disableToggle toggles one component in k3s.disable.
func disableToggle(component, help string) field {
	return field{
		label: "Disable " + component, help: help, kind: kindToggle,
		get: func(s *spec.ClusterSpec) string { return yesNo(contains(s.K3s.Disable, component)) },
		set: func(s *spec.ClusterSpec, v string) error {
			var out []string
			for _, d := range s.K3s.Disable {
				if d != component {
					out = append(out, d)
				}
			}
			if v == "yes" {
				out = append(out, component)
			}
			s.K3s.Disable = out
			return nil
		},
	}
}

func splitList(v string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// buildSections creates the wizard pages for the current spec. Pages that
// depend on the spec (node network) are rebuilt after every change.
func buildSections(s *spec.ClusterSpec, inv *pve.Inventory) []section {
	var tmplStores, rootStores, bridges, templates []string
	for _, st := range inv.Storages {
		if st.Active && st.Has("vztmpl") {
			tmplStores = append(tmplStores, st.ID)
		}
		if st.Active && st.Has("rootdir") {
			rootStores = append(rootStores, st.ID)
		}
	}
	for _, b := range inv.Bridges {
		bridges = append(bridges, b.Name)
	}
	for _, t := range inv.Templates {
		if strings.HasPrefix(t.VolID, s.Template.Storage+":") {
			templates = append(templates, t.Name())
		}
	}

	secs := []section{
		{
			title: "Cluster", prefixes: []string{"name", "hostname_prefix", "ctid_start", "node", "host"},
			intro: "Containers are named <prefix>-main1.. (servers) and <prefix>-node1.. (agents).",
			fields: []field{
				textField("Cluster name", "Used for state, tags (pk3s-<name>) and kubeconfig context", func(s *spec.ClusterSpec) *string { return &s.Name }),
				textField("Hostname prefix", "Prefix for container and k3s node names; empty = cluster name", func(s *spec.ClusterSpec) *string { return &s.HostnamePrefix }),
				intField("First CT ID", "Containers use consecutive IDs starting here", func(s *spec.ClusterSpec) *int { return &s.CTIDStart }),
			},
		},
		{
			title: "Topology", prefixes: []string{"servers", "agents"},
			intro: "1 server = SQLite. 3/5/7 servers = embedded etcd with quorum.\nAll containers on one Proxmox host is not host-level HA.",
			fields: []field{
				intField("Server nodes", "Control plane nodes: 1, 3, 5 or 7", func(s *spec.ClusterSpec) *int { return &s.Servers }),
				intField("Agent nodes", "Worker nodes", func(s *spec.ClusterSpec) *int { return &s.Agents }),
			},
		},
		{
			title: "Resources", prefixes: []string{"server_resources", "agent_resources"},
			intro: "Defaults are starting points, not sizing advice.\nk3s documents: server >= 2 CPU / 2 GiB, agent >= 1 CPU / 512 MiB.",
			fields: []field{
				intField("Server CPU cores", "", func(s *spec.ClusterSpec) *int { return &s.Server.Cores }),
				intField("Server RAM (MiB)", "", func(s *spec.ClusterSpec) *int { return &s.Server.MemoryMiB }),
				intField("Server disk (GiB)", "", func(s *spec.ClusterSpec) *int { return &s.Server.DiskGiB }),
				intField("Agent CPU cores", "", func(s *spec.ClusterSpec) *int { return &s.Agent.Cores }),
				intField("Agent RAM (MiB)", "", func(s *spec.ClusterSpec) *int { return &s.Agent.MemoryMiB }),
				intField("Agent disk (GiB)", "", func(s *spec.ClusterSpec) *int { return &s.Agent.DiskGiB }),
			},
		},
		{
			title: "Storage", prefixes: []string{"template", "rootfs_storage"},
			fields: []field{
				choiceField("Template storage", "Storage holding LXC templates (vztmpl)", tmplStores, false, func(s *spec.ClusterSpec) *string { return &s.Template.Storage }),
				choiceField("Template", "←/→ existing templates, Enter to type a name to download", templates, true, func(s *spec.ClusterSpec) *string { return &s.Template.Name }),
				toggleField("Download if missing", "Run 'pveam download' for a missing template (asks in the plan)", func(s *spec.ClusterSpec) *bool { return &s.Template.Download }),
				choiceField("Root disk storage", storageHelp(inv), rootStores, false, func(s *spec.ClusterSpec) *string { return &s.RootfsStorage }),
			},
		},
		{
			title: "Network", prefixes: []string{"network", "k3s.cluster_cidr", "k3s.service_cidr"},
			intro: "Defaults for all nodes. IPs are assigned consecutively from the first IP;\noverride any node on the next page.",
			fields: []field{
				choiceField("Bridge", "Linux bridge for eth0", bridges, false, func(s *spec.ClusterSpec) *string { return &s.Network.Bridge }),
				textField("First IP", "Static IPv4 of the first server", func(s *spec.ClusterSpec) *string { return &s.Network.FirstIP }),
				intField("Prefix length", "e.g. 24 for 255.255.255.0", func(s *spec.ClusterSpec) *int { return &s.Network.Prefix }),
				textField("Gateway", "", func(s *spec.ClusterSpec) *string { return &s.Network.Gateway }),
				listField("DNS servers", "", func(s *spec.ClusterSpec) *[]string { return &s.Network.DNS }),
				textField("Search domain", "", func(s *spec.ClusterSpec) *string { return &s.Network.SearchDomain }),
				intField("VLAN tag", "0 = untagged", func(s *spec.ClusterSpec) *int { return &s.Network.VLAN }),
				intField("MTU", "0 = bridge default", func(s *spec.ClusterSpec) *int { return &s.Network.MTU }),
			},
		},
		nodeNetworkSection(s),
		{
			title: "Persistent data", prefixes: []string{"persistent_data"},
			intro: "Optionally bind-mount a host directory into each container at\n/var/lib/rancher/k3s/storage (local-path-provisioner volumes).\nEach node gets <path>/<cluster>/<hostname>. Data survives container\ndestroy, is NOT included in vzdump backups and blocks CT migration.",
			fields: []field{
				toggleField("Use host directory", "", func(s *spec.ClusterSpec) *bool { return &s.Persistent.Enabled }),
				textField("Host base path", "e.g. /data/k3s-storage", func(s *spec.ClusterSpec) *string { return &s.Persistent.HostPath }),
			},
		},
		{
			title: "k3s", prefixes: []string{"k3s"},
			fields: []field{
				textField("Version", "Exact release (v1.37.2+k3s1) or channel (stable, latest, v1.36)", func(s *spec.ClusterSpec) *string { return &s.K3s.Version }),
				disableToggle("traefik", "Bundled ingress controller"),
				disableToggle("servicelb", "Bundled LoadBalancer (klipper-lb); disable if you use MetalLB/kube-vip"),
				listField("Extra TLS SANs", "Extra names/IPs for the API certificate", func(s *spec.ClusterSpec) *[]string { return &s.K3s.TLSSANs }),
				textField("Cluster CIDR", "Pod network", func(s *spec.ClusterSpec) *string { return &s.K3s.ClusterCIDR }),
				textField("Service CIDR", "Service network", func(s *spec.ClusterSpec) *string { return &s.K3s.ServiceCIDR }),
			},
		},
		{
			title: "OS & access", prefixes: []string{"os", "access"},
			intro: "No root password is set. Use 'pct enter <id>' or an SSH key.",
			fields: []field{
				toggleField("NFS client", "Install nfs-common; host loads nfs/nfsv4 modules", func(s *spec.ClusterSpec) *bool { return &s.OS.NFSClient }),
				listField("Extra packages", "Additional apt packages", func(s *spec.ClusterSpec) *[]string { return &s.OS.ExtraPackages }),
				textField("SSH public key file", "Host path, e.g. /root/.ssh/id_ed25519.pub", func(s *spec.ClusterSpec) *string { return &s.Access.SSHPublicKeyFile }),
				textField("Timezone", "'host' or IANA name", func(s *spec.ClusterSpec) *string { return &s.Access.Timezone }),
			},
		},
		{
			title: "Autostart", prefixes: []string{"startup"},
			intro: "Proxmox boot order. 'Delay' is how long Proxmox waits after starting\na container before starting the next one. Main (servers) start first;\nshutdown runs in reverse order, so main stops last.",
			fields: []field{
				toggleField("Start on boot", "Start containers when the Proxmox host boots", func(s *spec.ClusterSpec) *bool { return &s.OnBoot }),
				intField("Main start order", "Lower starts earlier and stops later", func(s *spec.ClusterSpec) *int { return &s.Startup.MainOrder }),
				intField("Delay after main (s)", "Wait before starting the next container", func(s *spec.ClusterSpec) *int { return &s.Startup.MainDelaySec }),
				intField("Node start order", "Must be greater than main", func(s *spec.ClusterSpec) *int { return &s.Startup.NodeOrder }),
				intField("Delay after each node (s)", "Staggers agent start-up", func(s *spec.ClusterSpec) *int { return &s.Startup.NodeDelaySec }),
			},
		},
		{
			title: "Security & host", prefixes: []string{"security", "host."},
			intro: "privileged: community-tested, but container root = host root.\nunprivileged-experimental: keeps isolation, not documented by k3s, may fail.",
			fields: []field{
				choiceField("LXC profile", "", []string{spec.ProfilePrivileged, spec.ProfileUnprivileged}, false, func(s *spec.ClusterSpec) *string { return &s.Security.Profile }),
				toggleField("I accept the privileged risk", "Pods escaping the container get root on this Proxmox host", func(s *spec.ClusterSpec) *bool { return &s.Security.AcknowledgePrivilegedRisk }),
				toggleField("Pass /dev/net/tun", "For VPN/WireGuard workloads", func(s *spec.ClusterSpec) *bool { return &s.Security.TunDevice }),
				toggleField("Enable FUSE", "Proxmox advises against it", func(s *spec.ClusterSpec) *bool { return &s.Security.Fuse }),
				toggleField("Load host kernel modules", "Write /etc/modules-load.d/pk3s.conf and modprobe", func(s *spec.ClusterSpec) *bool { return &s.Host.LoadKernelModules }),
			},
		},
	}
	return secs
}

func storageHelp(inv *pve.Inventory) string {
	var parts []string
	for _, st := range inv.Storages {
		if st.Active && st.Has("rootdir") {
			parts = append(parts, fmt.Sprintf("%s %s %d GiB free", st.ID, st.Type, st.Avail>>30))
		}
	}
	return strings.Join(parts, "; ")
}

// nodeNetworkSection exposes IP/prefix/gateway/DNS for every node. Values
// shown are the effective ones; editing creates a per-node override, and
// clearing a field returns it to the default.
func nodeNetworkSection(s *spec.ClusterSpec) section {
	sec := section{
		title:    "Node network",
		prefixes: []string{"nodes.", "node_network"},
		intro:    "Per-container networking. Empty value = cluster default (shown in brackets).",
	}
	defaultIP := func(i int) string { return nthIP(s.Network.FirstIP, i) }
	for i := 0; i < s.TotalNodes(); i++ {
		key := s.NodeKey(i)
		host := fmt.Sprintf("%s-%s", s.Prefix(), key)
		idx := i
		get := func(fn func(o spec.NodeNetwork) string) func(*spec.ClusterSpec) string {
			return func(s *spec.ClusterSpec) string {
				o, _ := s.Override(key)
				return fn(o)
			}
		}
		set := func(fn func(o *spec.NodeNetwork, v string) error) func(*spec.ClusterSpec, string) error {
			return func(s *spec.ClusterSpec, v string) error {
				o, _ := s.Override(key)
				o.Node = key
				if err := fn(&o, strings.TrimSpace(v)); err != nil {
					return err
				}
				s.SetOverride(o)
				return nil
			}
		}
		sec.fields = append(sec.fields,
			field{label: host + " IP", help: "default " + defaultIP(idx), kind: kindText,
				get: get(func(o spec.NodeNetwork) string { return o.IP }),
				set: set(func(o *spec.NodeNetwork, v string) error { o.IP = v; return nil })},
			field{label: host + " prefix", help: fmt.Sprintf("default /%d", s.Network.Prefix), kind: kindText,
				get: get(func(o spec.NodeNetwork) string {
					if o.Prefix == 0 {
						return ""
					}
					return strconv.Itoa(o.Prefix)
				}),
				set: set(func(o *spec.NodeNetwork, v string) error {
					if v == "" {
						o.Prefix = 0
						return nil
					}
					n, err := atoi(v)
					o.Prefix = n
					return err
				})},
			field{label: host + " gateway", help: "default " + s.Network.Gateway, kind: kindText,
				get: get(func(o spec.NodeNetwork) string { return o.Gateway }),
				set: set(func(o *spec.NodeNetwork, v string) error { o.Gateway = v; return nil })},
			field{label: host + " DNS", help: "default " + strings.Join(s.Network.DNS, ", "), kind: kindText,
				get: get(func(o spec.NodeNetwork) string { return strings.Join(o.DNS, ", ") }),
				set: set(func(o *spec.NodeNetwork, v string) error { o.DNS = splitList(v); return nil })},
		)
	}
	return sec
}

// nthIP returns first+i for display purposes ("" if first is invalid).
func nthIP(first string, i int) string {
	parts := strings.Split(strings.TrimSpace(first), ".")
	if len(parts) != 4 {
		return "?"
	}
	last, err := strconv.Atoi(parts[3])
	if err != nil {
		return "?"
	}
	return fmt.Sprintf("%s.%s.%s.%d", parts[0], parts[1], parts[2], last+i)
}
