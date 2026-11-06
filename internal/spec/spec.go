// Package spec defines the user-facing cluster specification, its defaults
// and YAML (de)serialisation. It contains no I/O besides file load/save.
package spec

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Security profile identifiers.
const (
	ProfilePrivileged   = "privileged"
	ProfileUnprivileged = "unprivileged-experimental"
)

// Default values. CPU/RAM are editable starting points, not sizing advice.
const (
	DefaultCores       = 2
	DefaultMemoryMiB   = 2048
	DefaultDiskGiB     = 30
	DefaultCTIDStart   = 5000
	DefaultChannel     = "stable"
	DefaultClusterCIDR = "10.42.0.0/16"
	DefaultServiceCIDR = "10.43.0.0/16"
	DefaultDataSubdir  = "/var/lib/rancher/k3s/storage"
)

// Resources per container.
type Resources struct {
	Cores     int `yaml:"cores"`
	MemoryMiB int `yaml:"memory_mib"`
	DiskGiB   int `yaml:"disk_gib"`
}

// Template selects the LXC OS template.
type Template struct {
	// Storage holding the template (content type vztmpl).
	Storage string `yaml:"storage"`
	// Name is the template file name, e.g. debian-13-standard_13.1-2_amd64.tar.zst.
	Name string `yaml:"name"`
	// Download allows pk3s to run `pveam download` if the template is missing.
	Download bool `yaml:"download"`
}

// VolID returns the Proxmox volume ID of the template.
func (t Template) VolID() string { return t.Storage + ":vztmpl/" + t.Name }

// Network describes node addressing. Node IPs are allocated sequentially
// starting at FirstIP: servers first, then agents.
type Network struct {
	Bridge       string   `yaml:"bridge"`
	VLAN         int      `yaml:"vlan,omitempty"`
	MTU          int      `yaml:"mtu,omitempty"`
	FirstIP      string   `yaml:"first_ip"`
	Prefix       int      `yaml:"prefix"`
	Gateway      string   `yaml:"gateway"`
	DNS          []string `yaml:"dns"`
	SearchDomain string   `yaml:"search_domain,omitempty"`
}

// NodeNetwork overrides addressing for one node. Node keys are role and
// ordinal: main1, main2, ... for servers and node1, node2, ... for agents. Empty fields
// fall back to the cluster-wide Network settings.
type NodeNetwork struct {
	Node    string   `yaml:"node"`
	IP      string   `yaml:"ip,omitempty"`
	Prefix  int      `yaml:"prefix,omitempty"`
	Gateway string   `yaml:"gateway,omitempty"`
	DNS     []string `yaml:"dns,omitempty"`
}

// K3s settings.
type K3s struct {
	// Version is an exact release (v1.37.2+k3s1) or a channel name
	// (stable, latest, v1.36). Channels are resolved to a version at plan
	// time and the resolved version is recorded in the state.
	Version     string   `yaml:"version"`
	Disable     []string `yaml:"disable,omitempty"`
	TLSSANs     []string `yaml:"tls_sans,omitempty"`
	ClusterCIDR string   `yaml:"cluster_cidr"`
	ServiceCIDR string   `yaml:"service_cidr"`
}

// Security selects the LXC security profile.
type Security struct {
	Profile string `yaml:"profile"`
	// AcknowledgePrivilegedRisk must be true to use the privileged profile.
	AcknowledgePrivilegedRisk bool `yaml:"acknowledge_privileged_risk"`
	// TunDevice passes /dev/net/tun into containers (VPN / WireGuard pods).
	TunDevice bool `yaml:"tun_device"`
	// Fuse enables the LXC fuse feature. Proxmox advises against FUSE in
	// containers (freeze/backup issues); off by default.
	Fuse bool `yaml:"fuse"`
}

// Host-level changes pk3s may perform.
type Host struct {
	// LoadKernelModules writes /etc/modules-load.d/pk3s.conf and loads the
	// modules immediately.
	LoadKernelModules bool `yaml:"load_kernel_modules"`
}

// PersistentData optionally bind-mounts a host directory into every
// container at the local-path-provisioner location.
type PersistentData struct {
	Enabled bool `yaml:"enabled"`
	// HostPath is the base directory; each node gets HostPath/<cluster>/<hostname>.
	HostPath string `yaml:"host_path,omitempty"`
}

// Access options for the containers.
type Access struct {
	// SSHPublicKeyFile is injected into root's authorized_keys. No root
	// password is ever set; use `pct enter <id>` from the host.
	SSHPublicKeyFile string `yaml:"ssh_public_key_file,omitempty"`
	// Timezone: "host" (copy from host) or an IANA name like Europe/Kyiv.
	Timezone string `yaml:"timezone,omitempty"`
}

// OS options for the container operating system.
type OS struct {
	// NFSClient installs nfs-common so pods can mount NFS volumes and the
	// host loads the nfs/nfsv4 kernel modules.
	NFSClient bool `yaml:"nfs_client"`
	// ExtraPackages are additional apt packages installed on every node.
	ExtraPackages []string `yaml:"extra_packages,omitempty"`
}

// Startup controls Proxmox boot ordering (`startup: order=N,up=S`). Servers
// (main) start first; agents (nodes) follow. "up" is how long Proxmox waits
// after starting a container before starting the next one.
// Proxmox shuts guests down in reverse order, so main containers stop last.
type Startup struct {
	MainOrder    int `yaml:"main_order"`
	MainDelaySec int `yaml:"main_delay_sec"`
	NodeOrder    int `yaml:"node_order"`
	NodeDelaySec int `yaml:"node_delay_sec"`
}

// ClusterSpec is the complete user input.
type ClusterSpec struct {
	Name string `yaml:"name"`
	// HostnamePrefix names containers and k3s nodes: <prefix>-main1.. for
	// servers and <prefix>-node1.. for agents.
	// Empty means the cluster name.
	HostnamePrefix string         `yaml:"hostname_prefix,omitempty"`
	Node           string         `yaml:"node"`
	Servers        int            `yaml:"servers"`
	Agents         int            `yaml:"agents"`
	CTIDStart      int            `yaml:"ctid_start"`
	Template       Template       `yaml:"template"`
	RootfsStorage  string         `yaml:"rootfs_storage"`
	Server         Resources      `yaml:"server_resources"`
	Agent          Resources      `yaml:"agent_resources"`
	Network        Network        `yaml:"network"`
	NodeNetwork    []NodeNetwork  `yaml:"node_network,omitempty"`
	K3s            K3s            `yaml:"k3s"`
	Security       Security       `yaml:"security"`
	Host           Host           `yaml:"host"`
	Persistent     PersistentData `yaml:"persistent_data"`
	Access         Access         `yaml:"access"`
	OS             OS             `yaml:"os"`
	OnBoot         bool           `yaml:"onboot"`
	Startup        Startup        `yaml:"startup"`
}

// Default returns a spec populated with defaults. Environment-specific
// values (storage, bridge, addresses) are left empty or filled from
// inventory by the caller.
func Default() ClusterSpec {
	res := Resources{Cores: DefaultCores, MemoryMiB: DefaultMemoryMiB, DiskGiB: DefaultDiskGiB}
	return ClusterSpec{
		Name:      "k3s",
		Servers:   1,
		Agents:    2,
		CTIDStart: DefaultCTIDStart,
		Server:    res,
		Agent:     res,
		Network:   Network{Bridge: "vmbr0", Prefix: 24},
		K3s: K3s{
			Version:     DefaultChannel,
			ClusterCIDR: DefaultClusterCIDR,
			ServiceCIDR: DefaultServiceCIDR,
		},
		Security: Security{Profile: ProfilePrivileged},
		Host:     Host{LoadKernelModules: true},
		Access:   Access{Timezone: "host"},
		OS:       OS{NFSClient: true},
		OnBoot:   true,
		Startup:  Startup{MainOrder: 1, MainDelaySec: 10, NodeOrder: 2, NodeDelaySec: 10},
	}
}

// Prefix returns the effective hostname prefix.
func (s ClusterSpec) Prefix() string {
	if s.HostnamePrefix != "" {
		return s.HostnamePrefix
	}
	return s.Name
}

// NodeKey returns the override key for the i-th node (servers first).
func (s ClusterSpec) NodeKey(i int) string {
	if i < s.Servers {
		return fmt.Sprintf("main%d", i+1)
	}
	return fmt.Sprintf("node%d", i-s.Servers+1)
}

// Override returns the network override for a node key, if any.
func (s ClusterSpec) Override(key string) (NodeNetwork, bool) {
	for _, o := range s.NodeNetwork {
		if o.Node == key {
			return o, true
		}
	}
	return NodeNetwork{}, false
}

// SetOverride replaces or adds the override for o.Node; an override with
// no fields set is removed.
func (s *ClusterSpec) SetOverride(o NodeNetwork) {
	var out []NodeNetwork
	for _, x := range s.NodeNetwork {
		if x.Node != o.Node {
			out = append(out, x)
		}
	}
	if o.IP != "" || o.Prefix != 0 || o.Gateway != "" || len(o.DNS) > 0 {
		out = append(out, o)
	}
	s.NodeNetwork = out
}

// TotalNodes returns servers + agents.
func (s ClusterSpec) TotalNodes() int { return s.Servers + s.Agents }

// Load reads a spec from a YAML file, applying defaults for missing fields.
func Load(path string) (ClusterSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ClusterSpec{}, err
	}
	return Parse(data)
}

// Parse decodes YAML on top of defaults. Unknown fields are rejected so
// typos do not silently fall back to defaults.
func Parse(data []byte) (ClusterSpec, error) {
	s := Default()
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return ClusterSpec{}, fmt.Errorf("parse spec: %w", err)
	}
	return s, nil
}

// Marshal encodes the spec as YAML.
func Marshal(s ClusterSpec) ([]byte, error) { return yaml.Marshal(s) }

// Save writes the spec to path (0600: it may reveal network layout).
func Save(path string, s ClusterSpec) error {
	data, err := Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
