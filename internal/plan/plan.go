// Package plan turns a ClusterSpec and host Inventory into a concrete,
// printable deployment plan. It performs no I/O.
package plan

import (
	"fmt"
	"net/netip"
	"path"
	"strings"

	"github.com/oleksdovz/pk3s/internal/k3s"
	"github.com/oleksdovz/pk3s/internal/lxcprofile"
	"github.com/oleksdovz/pk3s/internal/pve"
	"github.com/oleksdovz/pk3s/internal/spec"
)

// Node is one planned container.
type Node struct {
	Index     int    // 0-based across all nodes
	Key       string // main1, main2, node1, ...
	Role      k3s.Role
	Bootstrap bool // first server
	Hostname  string
	CTID      int
	IP        string
	Prefix    int
	Gateway   string
	DNS       []string
	// Overridden marks nodes with per-node network settings.
	Overridden bool
	Resources  spec.Resources
	// DataDir is the host bind-mount source (empty if persistent data is off).
	DataDir string
}

// HostChange is a modification to the Proxmox host outside of containers.
type HostChange struct {
	Description string
	Path        string
}

// StepKind identifies what a step does.
type StepKind string

// Step kinds.
const (
	StepHostModules  StepKind = "host-modules"
	StepTemplate     StepKind = "template"
	StepArtifact     StepKind = "k3s-artifact"
	StepCreateCT     StepKind = "ct-create"
	StepConfigureCT  StepKind = "ct-configure"
	StepStartCT      StepKind = "ct-start"
	StepPrepareNode  StepKind = "node-prepare"
	StepInstallK3s   StepKind = "k3s-install"
	StepWaitNode     StepKind = "k3s-wait"
	StepFetchTokens  StepKind = "k3s-tokens"
	StepClusterReady StepKind = "cluster-ready"
	StepKubeconfig   StepKind = "kubeconfig"
	// Update operations.
	StepUpdateCT    StepKind = "ct-update"
	StepBackup      StepKind = "datastore-backup"
	StepUpgradeNode StepKind = "k3s-upgrade"
	StepRemoveNode  StepKind = "node-remove"
	// StepPublishState writes the cluster state ConfigMap (create and update).
	StepPublishState StepKind = "publish-state"

	// Merged activities (one entry per container in the step list).
	StepProvision StepKind = "provision" // create, profile, start, OS packages
	StepBootstrap StepKind = "bootstrap" // k3s on main1, readiness, join tokens
	StepJoin      StepKind = "join"      // k3s on another node, readiness
	StepFinalize  StepKind = "finalize"  // all Ready, kubeconfig, state ConfigMap
)

// MaxParallel bounds steps of one parallel group running at once.
const MaxParallel = 4

// Step is one unit of work; ID is stable across runs and used for resume.
type Step struct {
	ID    string
	Kind  StepKind
	Title string
	// Node indexes Plan.Nodes (Plan.Removed for StepRemoveNode); -1 if
	// the step is not node-specific.
	Node int
	// Group: consecutive steps with the same non-empty group run in
	// parallel (at most MaxParallel at a time).
	Group string
}

// Plan is the full deployment plan.
type Plan struct {
	Spec        spec.ClusterSpec
	K3sVersion  string
	Arch        string
	Endpoint    string
	Datastore   string
	Profile     lxcprofile.Profile
	Nodes       []Node
	HostChanges []HostChange
	Steps       []Step
	Tags        []string
	// MissingModules are host kernel modules that are not loaded.
	MissingModules []string

	// Update mode (zero values for a new cluster).
	Mode        string // ModeCreate | ModeUpdate
	OpID        string
	FromVersion string
	Existing    map[string]bool // node keys that already exist
	Removed     []Node          // nodes to remove (highest number first)
	Changes     []string        // human-readable summary of the diff
}

// Plan modes.
const (
	ModeCreate = "create"
	ModeUpdate = "update"
)

// ClusterTag returns the per-cluster Proxmox tag.
func ClusterTag(name string) string { return "pk3s-" + name }

// ManagedTag marks all containers created by pk3s.
const ManagedTag = "pk3s"

// Build computes the plan. k3sVersion must be an exact version (channels
// are resolved by the caller). Build fails only on errors that make a plan
// impossible to compute; everything else is reported by validate.
func Build(s spec.ClusterSpec, inv *pve.Inventory, k3sVersion string) (*Plan, error) {
	return BuildLayout(s, inv, k3sVersion, nil)
}

// BuildLayout is Build with an existing node layout: nodes found in layout
// keep their recorded CT ID, address and data directory; new nodes get the
// lowest free CT ID and address at or after their default position.
func BuildLayout(s spec.ClusterSpec, inv *pve.Inventory, k3sVersion string, layout []NodeRecord) (*Plan, error) {
	prof, err := lxcprofile.For(s.Security)
	if err != nil {
		return nil, err
	}
	first, err := netip.ParseAddr(strings.TrimSpace(s.Network.FirstIP))
	if err != nil {
		return nil, fmt.Errorf("network.first_ip: %w", err)
	}
	if !first.Is4() {
		return nil, fmt.Errorf("network.first_ip: only IPv4 is supported in this version")
	}
	p := &Plan{
		Spec:       s,
		K3sVersion: k3sVersion,
		Arch:       inv.Arch,
		Profile:    prof,
		Tags:       []string{ManagedTag, ClusterTag(s.Name)},
		Mode:       ModeCreate,
		Existing:   map[string]bool{},
	}
	recs := map[string]NodeRecord{}
	usedID := map[int]bool{}
	usedIP := map[netip.Addr]bool{}
	for _, r := range layout {
		recs[r.Key] = r
		usedID[r.CTID] = true
		if a, err := netip.ParseAddr(r.IP); err == nil {
			usedIP[a] = true
		}
	}
	for _, o := range s.NodeNetwork {
		if _, pinned := recs[o.Node]; pinned {
			continue
		}
		if a, err := netip.ParseAddr(strings.TrimSpace(o.IP)); err == nil {
			usedIP[a] = true
		}
	}
	// Always embedded etcd, also for a single server: adding servers later
	// is a plain join and etcd snapshots are available for backups.
	if s.Servers == 1 {
		p.Datastore = "embedded etcd, 1 server (no control-plane fault tolerance; grow to 3 servers to tolerate 1 failure)"
	} else {
		p.Datastore = fmt.Sprintf("embedded etcd (%d servers, quorum %d, tolerates %d server failure(s))",
			s.Servers, s.Servers/2+1, (s.Servers-1)/2)
	}

	for i := 0; i < s.TotalNodes(); i++ {
		key := s.NodeKey(i)
		n := Node{Index: i, Key: key, Hostname: s.Prefix() + "-" + key}
		if i < s.Servers {
			n.Role, n.Resources, n.Bootstrap = k3s.RoleServer, s.Server, i == 0
		} else {
			n.Role, n.Resources = k3s.RoleAgent, s.Agent
		}
		if r, ok := recs[key]; ok {
			// Existing node: identity and network never change.
			p.Existing[key] = true
			n.CTID, n.IP, n.Prefix, n.Gateway, n.DNS, n.DataDir = r.CTID, r.IP, r.Prefix, r.Gateway, r.DNS, r.DataDir
			p.Nodes = append(p.Nodes, n)
			continue
		}
		id := s.CTIDStart + i
		for usedID[id] {
			id++
		}
		usedID[id] = true
		n.CTID = id
		n.Prefix, n.Gateway, n.DNS = s.Network.Prefix, s.Network.Gateway, s.Network.DNS
		o, hasOverride := s.Override(key)
		if hasOverride && o.IP != "" {
			n.IP = strings.TrimSpace(o.IP)
		} else {
			ip := nthAddr(first, i)
			for usedIP[ip] {
				ip = ip.Next()
			}
			usedIP[ip] = true
			n.IP = ip.String()
		}
		if hasOverride {
			n.Overridden = true
			if o.Prefix != 0 {
				n.Prefix = o.Prefix
			}
			if o.Gateway != "" {
				n.Gateway = o.Gateway
			}
			if len(o.DNS) > 0 {
				n.DNS = o.DNS
			}
		}
		if s.Persistent.Enabled && s.Persistent.HostPath != "" {
			n.DataDir = path.Join(s.Persistent.HostPath, s.Name, n.Hostname)
		}
		p.Nodes = append(p.Nodes, n)
	}
	for k := range overrideKeys(s) {
		if !validKey(s, k) {
			return nil, fmt.Errorf("node_network: unknown node %q (valid: main1..main%d, node1..node%d)", k, s.Servers, s.Agents)
		}
	}
	p.Endpoint = p.Nodes[0].IP

	p.MissingModules = lxcprofile.MissingModules(lxcprofile.ModulesFor(s.OS.NFSClient), inv.LoadedModules)
	if s.Host.LoadKernelModules {
		p.HostChanges = append(p.HostChanges, HostChange{
			Description: "Persist k3s kernel modules (overlay, br_netfilter, vxlan, ...) and load missing ones: " + orNone(p.MissingModules),
			Path:        lxcprofile.ModulesLoadFile,
		})
	}
	for _, n := range p.Nodes {
		if n.DataDir != "" {
			p.HostChanges = append(p.HostChanges, HostChange{
				Description: fmt.Sprintf("Create data directory for %s (bind-mounted at %s)", n.Hostname, k3s.LocalStoragePath),
				Path:        n.DataDir,
			})
		}
	}
	if !inv.HasTemplate(s.Template.VolID()) {
		p.HostChanges = append(p.HostChanges, HostChange{
			Description: "Download LXC template " + s.Template.Name,
			Path:        s.Template.VolID(),
		})
	}
	p.HostChanges = append(p.HostChanges, HostChange{
		Description: "Cache k3s " + k3sVersion + " binary and checksum file",
		Path:        "/var/lib/pk3s/cache/k3s/" + k3sVersion,
	})

	p.Steps = buildSteps(p, inv)
	return p, nil
}

func nthAddr(a netip.Addr, n int) netip.Addr {
	for ; n > 0; n-- {
		a = a.Next()
	}
	return a
}

func overrideKeys(s spec.ClusterSpec) map[string]bool {
	m := map[string]bool{}
	for _, o := range s.NodeNetwork {
		m[o.Node] = true
	}
	return m
}

func validKey(s spec.ClusterSpec, k string) bool {
	for i := 0; i < s.TotalNodes(); i++ {
		if s.NodeKey(i) == k {
			return true
		}
	}
	return false
}

func orNone(m []string) string {
	if len(m) == 0 {
		return "none missing"
	}
	return strings.Join(m, ", ")
}

func buildSteps(p *Plan, inv *pve.Inventory) []Step {
	var steps []Step
	add := func(kind StepKind, node int, group, title string) {
		id := string(kind)
		if node >= 0 {
			id = fmt.Sprintf("%s/%s", kind, p.Nodes[node].Hostname)
		}
		steps = append(steps, Step{ID: id, Kind: kind, Title: title, Node: node, Group: group})
	}
	if p.Spec.Host.LoadKernelModules {
		add(StepHostModules, -1, "", "Load host kernel modules")
	}
	if !inv.HasTemplate(p.Spec.Template.VolID()) {
		add(StepTemplate, -1, "", "Download template "+p.Spec.Template.Name)
	}
	add(StepArtifact, -1, "", "Download and verify k3s "+p.K3sVersion)
	steps = append(steps, nodeSteps(p, p.Nodes, "")...)
	add(StepFinalize, -1, "", "Finalize: all nodes Ready, kubeconfig, state ConfigMap")
	return steps
}

// nodeSteps provisions nodes in parallel, bootstraps main1 if it is among
// them, joins servers one at a time (etcd) and agents in parallel.
// prefix scopes step IDs (update operations).
func nodeSteps(p *Plan, nodes []Node, prefix string) []Step {
	var steps []Step
	add := func(kind StepKind, n Node, group, title string) {
		steps = append(steps, Step{ID: prefix + string(kind) + "/" + n.Hostname, Kind: kind, Title: title, Node: n.Index, Group: group})
	}
	for _, n := range nodes {
		add(StepProvision, n, "provision", fmt.Sprintf("Provision %s (CT %d): create, start, OS packages", n.Hostname, n.CTID))
	}
	for _, n := range nodes {
		if n.Bootstrap {
			add(StepBootstrap, n, "", fmt.Sprintf("Bootstrap k3s server on %s (CT %d)", n.Hostname, n.CTID))
		}
	}
	for _, n := range nodes {
		if n.Role == k3s.RoleServer && !n.Bootstrap {
			add(StepJoin, n, "", fmt.Sprintf("Join server %s (CT %d)", n.Hostname, n.CTID))
		}
	}
	for _, n := range nodes {
		if n.Role == k3s.RoleAgent {
			add(StepJoin, n, "join-agents", fmt.Sprintf("Join agent %s (CT %d)", n.Hostname, n.CTID))
		}
	}
	return steps
}

// ServerURL is the registration address for joining nodes.
func (p *Plan) ServerURL() string { return "https://" + p.Endpoint + ":6443" }

// TLSSANs returns SANs for the API server certificate.
func (p *Plan) TLSSANs() []string {
	sans := []string{p.Endpoint}
	for _, n := range p.Nodes {
		if n.Role == k3s.RoleServer && n.IP != p.Endpoint {
			sans = append(sans, n.IP)
		}
	}
	return append(sans, p.Spec.K3s.TLSSANs...)
}

// NodeConfig renders the k3s config for a node. Tokens are supplied by the
// caller; joinToken is the secure (K10...) token for joining nodes.
func (p *Plan) NodeConfig(n Node, serverToken, agentToken, joinToken string) k3s.NodeConfig {
	c := k3s.NodeConfig{
		NodeIP:       n.IP,
		NodeName:     n.Hostname,
		KubeletArg:   p.Profile.KubeletArgs,
		KubeProxyArg: p.Profile.KubeProxyArgs,
	}
	if n.Role == k3s.RoleAgent {
		c.Server = p.ServerURL()
		c.Token = joinToken
		return c
	}
	c.TLSSAN = p.TLSSANs()
	c.Disable = p.Spec.K3s.Disable
	c.ClusterCIDR = p.Spec.K3s.ClusterCIDR
	c.ServiceCIDR = p.Spec.K3s.ServiceCIDR
	c.WriteKubeconfigMode = "0600"
	c.AgentToken = agentToken
	if n.Bootstrap {
		c.Token = serverToken
		c.ClusterInit = true
	} else {
		c.Server = p.ServerURL()
		c.Token = joinToken
	}
	return c
}

// StartupFor returns the Proxmox startup spec for a node. Proxmox starts
// guests by ascending order and shuts them down in reverse, so main
// (servers) start first and stop last.
func (p *Plan) StartupFor(n Node) string {
	st := p.Spec.Startup
	if n.Role == k3s.RoleServer {
		return fmt.Sprintf("order=%d,up=%d", st.MainOrder, st.MainDelaySec)
	}
	return fmt.Sprintf("order=%d,up=%d", st.NodeOrder, st.NodeDelaySec)
}

// CreateOpts builds pct create options for a node.
func (p *Plan) CreateOpts(n Node) pve.CreateOpts {
	s := p.Spec
	tz := s.Access.Timezone
	return pve.CreateOpts{
		VMID:         n.CTID,
		Template:     s.Template.VolID(),
		Hostname:     n.Hostname,
		Cores:        n.Resources.Cores,
		MemoryMiB:    n.Resources.MemoryMiB,
		RootfsStore:  s.RootfsStorage,
		DiskGiB:      n.Resources.DiskGiB,
		Bridge:       s.Network.Bridge,
		VLAN:         s.Network.VLAN,
		MTU:          s.Network.MTU,
		IP:           n.IP,
		Prefix:       n.Prefix,
		Gateway:      n.Gateway,
		DNS:          n.DNS,
		SearchDomain: s.Network.SearchDomain,
		Unprivileged: p.Profile.Unprivileged,
		Features:     p.Profile.Features,
		Tags:         p.Tags,
		Description:  fmt.Sprintf("Managed by pk3s. Cluster: %s, role: %s.", s.Name, n.Role),
		OnBoot:       s.OnBoot,
		Startup:      p.StartupFor(n),
		Timezone:     tz,
		SSHKeyFile:   s.Access.SSHPublicKeyFile,
	}
}
