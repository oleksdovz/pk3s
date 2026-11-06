// Package validate checks a spec and plan against the host inventory and
// documented k3s requirements. Checks are pure; anything that touches the
// network or filesystem is injected through Probes.
package validate

import (
	"fmt"
	"net/netip"
	"path"
	"regexp"
	"strings"

	"github.com/oleksdovz/pk3s/internal/k3s"
	"github.com/oleksdovz/pk3s/internal/plan"
	"github.com/oleksdovz/pk3s/internal/pve"
	"github.com/oleksdovz/pk3s/internal/spec"
)

// Severity of a finding.
type Severity int

// Severities. Errors block deployment.
const (
	Info Severity = iota
	Warning
	Error
)

func (s Severity) String() string {
	switch s {
	case Error:
		return "ERROR"
	case Warning:
		return "WARN"
	default:
		return "INFO"
	}
}

// Finding is one validation result. Field uses the YAML path of the spec.
type Finding struct {
	Severity Severity
	Field    string
	Message  string
}

func (f Finding) String() string { return fmt.Sprintf("[%s] %s: %s", f.Severity, f.Field, f.Message) }

// Findings is a list of findings.
type Findings []Finding

// HasErrors reports whether any finding blocks deployment.
func (fs Findings) HasErrors() bool {
	for _, f := range fs {
		if f.Severity == Error {
			return true
		}
	}
	return false
}

// ForField returns findings whose field starts with prefix.
func (fs Findings) ForField(prefix string) Findings {
	var out Findings
	for _, f := range fs {
		if strings.HasPrefix(f.Field, prefix) {
			out = append(out, f)
		}
	}
	return out
}

// Probes are optional side-effecting checks. Nil probes are skipped.
type Probes struct {
	// IPInUse reports whether an address answers on the network.
	IPInUse func(ip string) bool
	// DirNonEmpty reports whether a host directory exists and is not empty.
	DirNonEmpty func(path string) bool
	// StateExists reports whether pk3s already has state for the cluster.
	StateExists func(name string) bool
}

// Documented k3s minimums (https://docs.k3s.io/installation/requirements).
const (
	minServerMemMiB   = 2048
	sizingServerMiB   = 4096
	minServerCores    = 2
	minAgentMemMiB    = 512
	minDiskGiB        = 8
	recommendedDiskGB = 16
)

var pkgRe = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]+$`)
var nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}[a-z0-9]$`)

// Spec validates fields that do not need a plan. It is safe to call on
// partially filled specs (used live by the TUI).
func Spec(s spec.ClusterSpec, inv *pve.Inventory) Findings {
	var f Findings
	add := func(sev Severity, field, msg string, args ...any) {
		f = append(f, Finding{sev, field, fmt.Sprintf(msg, args...)})
	}

	if !nameRe.MatchString(s.Name) {
		add(Error, "name", "must be 2-32 chars of lowercase letters, digits and '-', starting with a letter")
	}
	if s.HostnamePrefix != "" && !nameRe.MatchString(s.HostnamePrefix) {
		add(Error, "hostname_prefix", "must be 2-32 chars of lowercase letters, digits and '-', starting with a letter")
	}

	// Topology and datastore.
	switch {
	case s.Servers < 1:
		add(Error, "servers", "at least one server node is required")
	case s.Servers == 2:
		add(Error, "servers", "2 servers with embedded etcd have quorum 2: losing either server stops the control plane. Use 1 or 3")
	case s.Servers > 1 && s.Servers%2 == 0:
		add(Error, "servers", "embedded etcd requires an odd number of servers (quorum = n/2+1)")
	case s.Servers > 7:
		add(Warning, "servers", "more than 7 etcd members increases write latency without practical benefit")
	}
	if s.Servers >= 3 {
		add(Info, "servers", "embedded etcd tolerates %d server container failure(s); all containers on one Proxmox host is NOT host-level HA", (s.Servers-1)/2)
	}
	if s.Agents < 0 {
		add(Error, "agents", "cannot be negative")
	}
	if s.Agents == 0 && s.Servers >= 1 {
		add(Info, "agents", "no agents: workloads will run on server nodes")
	}
	if s.CTIDStart < 100 {
		add(Error, "ctid_start", "Proxmox CT IDs start at 100")
	}

	// Resources (documented minimums; defaults are not sizing advice).
	checkRes(&f, "server_resources", s.Server, true)
	if s.Agents > 0 {
		checkRes(&f, "agent_resources", s.Agent, false)
	}

	// k3s.
	if !k3s.IsExactVersion(s.K3s.Version) && !k3s.IsChannel(s.K3s.Version) {
		add(Error, "k3s.version", "must be a release like v1.37.2+k3s1 or a channel (stable, latest, v1.37)")
	}
	for _, d := range s.K3s.Disable {
		switch d {
		case "traefik", "servicelb", "local-storage", "metrics-server", "coredns":
		default:
			add(Error, "k3s.disable", "unknown component %q", d)
		}
	}
	cc, err1 := netip.ParsePrefix(s.K3s.ClusterCIDR)
	if err1 != nil {
		add(Error, "k3s.cluster_cidr", "invalid CIDR")
	}
	sc, err2 := netip.ParsePrefix(s.K3s.ServiceCIDR)
	if err2 != nil {
		add(Error, "k3s.service_cidr", "invalid CIDR")
	}
	if err1 == nil && err2 == nil && cc.Overlaps(sc) {
		add(Error, "k3s.service_cidr", "overlaps cluster_cidr")
	}

	// Security.
	switch s.Security.Profile {
	case spec.ProfilePrivileged:
		if !s.Security.AcknowledgePrivilegedRisk {
			add(Error, "security.acknowledge_privileged_risk", "privileged containers give pods a path to host root; acknowledge this risk to continue")
		}
	case spec.ProfileUnprivileged:
		add(Warning, "security.profile", "unprivileged k3s in LXC is experimental and not documented by k3s; deployment may fail")
	default:
		add(Error, "security.profile", "must be %q or %q", spec.ProfilePrivileged, spec.ProfileUnprivileged)
	}
	if s.Security.Fuse {
		add(Warning, "security.fuse", "Proxmox advises against FUSE in containers (freeze/backup problems)")
	}

	// Persistent data.
	if s.Persistent.Enabled {
		hp := s.Persistent.HostPath
		switch {
		case hp == "" || !path.IsAbs(hp):
			add(Error, "persistent_data.host_path", "must be an absolute path")
		case path.Clean(hp) == "/" || strings.HasPrefix(path.Clean(hp), "/etc") || strings.HasPrefix(path.Clean(hp), "/proc") ||
			strings.HasPrefix(path.Clean(hp), "/sys") || strings.HasPrefix(path.Clean(hp), "/dev"):
			add(Error, "persistent_data.host_path", "refusing system path %s", hp)
		}
		add(Info, "persistent_data", "bind mounts are excluded from vzdump backups and prevent CT migration; back up the host directory separately")
	}

	// OS packages. Names end up in a shell script, so they are restricted
	// to the Debian package name grammar.
	for _, pkg := range s.OS.ExtraPackages {
		if !pkgRe.MatchString(pkg) {
			add(Error, "os.extra_packages", "invalid package name %q", pkg)
		}
	}
	if s.OS.NFSClient && s.Security.Profile == spec.ProfileUnprivileged {
		add(Warning, "os.nfs_client", "the kernel does not allow NFS mounts inside unprivileged containers; NFS volumes will fail")
	}

	// Startup ordering.
	st := s.Startup
	if st.MainOrder < 0 || st.NodeOrder < 0 || st.MainDelaySec < 0 || st.NodeDelaySec < 0 {
		add(Error, "startup", "order and delays must not be negative")
	}
	if st.MainDelaySec > 3600 || st.NodeDelaySec > 3600 {
		add(Error, "startup", "delays above 3600 seconds are not useful")
	}
	if s.Agents > 0 && st.NodeOrder <= st.MainOrder {
		add(Warning, "startup", "node_order should be greater than main_order so main starts first and stops last")
	}
	if !s.OnBoot {
		add(Info, "startup", "onboot is off: the cluster will not start automatically with the Proxmox host")
	}

	// Access.
	if tz := s.Access.Timezone; tz != "" && tz != "host" && !strings.Contains(tz, "/") && tz != "UTC" {
		add(Warning, "access.timezone", "expected 'host' or an IANA zone like Europe/Kyiv")
	}

	if inv != nil {
		checkInventory(&f, s, inv)
	}
	return f
}

func checkRes(f *Findings, field string, r spec.Resources, server bool) {
	add := func(sev Severity, sub, msg string, args ...any) {
		*f = append(*f, Finding{sev, field + "." + sub, fmt.Sprintf(msg, args...)})
	}
	if r.Cores < 1 {
		add(Error, "cores", "must be at least 1")
	} else if server && r.Cores < minServerCores {
		add(Warning, "cores", "k3s documents a minimum of %d cores for servers", minServerCores)
	}
	if server {
		switch {
		case r.MemoryMiB < minServerMemMiB:
			add(Error, "memory_mib", "k3s documents a minimum of %d MiB for servers", minServerMemMiB)
		case r.MemoryMiB < sizingServerMiB:
			add(Warning, "memory_mib", "k3s sizing guidance starts at 2 CPU / %d MiB per server; %d MiB leaves little room for workloads", sizingServerMiB, r.MemoryMiB)
		}
	} else if r.MemoryMiB < minAgentMemMiB {
		add(Error, "memory_mib", "k3s documents a minimum of %d MiB for agents", minAgentMemMiB)
	}
	switch {
	case r.DiskGiB < minDiskGiB:
		add(Error, "disk_gib", "at least %d GiB is needed for OS, k3s and system images", minDiskGiB)
	case r.DiskGiB < recommendedDiskGB:
		add(Warning, "disk_gib", "below %d GiB the node may hit image garbage collection quickly", recommendedDiskGB)
	}
}

var supportedTemplate = regexp.MustCompile(`^(debian-(12|13)|ubuntu-(22\.04|24\.04|26\.04))-standard_`)
var aptTemplate = regexp.MustCompile(`^(debian|ubuntu)-`)

func checkInventory(f *Findings, s spec.ClusterSpec, inv *pve.Inventory) {
	add := func(sev Severity, field, msg string, args ...any) {
		*f = append(*f, Finding{sev, field, fmt.Sprintf(msg, args...)})
	}
	if !inv.CgroupV2 {
		add(Error, "host", "host is not running pure cgroup v2")
	}
	if inv.Arch != "amd64" && inv.Arch != "arm64" {
		add(Error, "host", "unsupported architecture %q", inv.Arch)
	} else if inv.Arch == "arm64" {
		add(Warning, "host", "arm64 Proxmox is unofficial; pk3s has not been tested on it")
	}
	if inv.FirewallOn {
		add(Warning, "network", "Proxmox firewall is enabled: allow TCP 6443, 10250, 2379-2380 and UDP 8472 between nodes")
	}
	if s.Node != "" && s.Node != inv.Node {
		add(Error, "node", "multi-host placement is not supported yet; run pk3s on node %q or set node to %q", s.Node, inv.Node)
	}

	// Template.
	if s.Template.Storage == "" || s.Template.Name == "" {
		add(Error, "template", "select a template")
	} else {
		st, ok := inv.Storage(s.Template.Storage)
		switch {
		case !ok:
			add(Error, "template.storage", "storage %q not found", s.Template.Storage)
		case !st.Has("vztmpl"):
			add(Error, "template.storage", "storage %q does not hold container templates (vztmpl)", s.Template.Storage)
		}
		if !inv.HasTemplate(s.Template.VolID()) && !s.Template.Download {
			add(Error, "template.name", "%s not found; enable download or pick an existing template", s.Template.VolID())
		}
		switch {
		case !aptTemplate.MatchString(s.Template.Name):
			add(Error, "template.name", "only Debian/Ubuntu templates are supported (node preparation uses apt and systemd)")
		case !supportedTemplate.MatchString(s.Template.Name):
			add(Warning, "template.name", "template %s is not in the tested list (Debian 12/13, Ubuntu LTS)", s.Template.Name)
		}
		if inv.Arch != "" && !strings.Contains(s.Template.Name, "_"+inv.Arch) {
			add(Warning, "template.name", "template name does not mention host architecture %s", inv.Arch)
		}
	}

	// Rootfs storage and capacity.
	st, ok := inv.Storage(s.RootfsStorage)
	switch {
	case s.RootfsStorage == "":
		add(Error, "rootfs_storage", "select a storage for container disks")
	case !ok:
		add(Error, "rootfs_storage", "storage %q not found", s.RootfsStorage)
	case !st.Has("rootdir"):
		add(Error, "rootfs_storage", "storage %q does not allow container root disks (rootdir)", s.RootfsStorage)
	case !st.Active:
		add(Error, "rootfs_storage", "storage %q is not active", s.RootfsStorage)
	default:
		need := int64(s.Servers*s.Server.DiskGiB+s.Agents*s.Agent.DiskGiB) << 30
		if need > st.Avail {
			add(Error, "rootfs_storage", "needs %d GiB, only %d GiB available", need>>30, st.Avail>>30)
		} else if need > st.Avail*8/10 {
			add(Warning, "rootfs_storage", "deployment uses more than 80%% of free space")
		}
		if st.Type == "zfspool" {
			add(Warning, "rootfs_storage", "ZFS root disks: containerd overlayfs needs OpenZFS >= 2.2; verify on this host")
		}
		if st.Type == "dir" || st.Type == "nfs" || st.Type == "cifs" {
			add(Warning, "rootfs_storage", "%s storage uses raw image files; LVM-thin or ZFS is preferred for etcd I/O", st.Type)
		}
	}

	// Bridge.
	if _, ok := inv.Bridge(s.Network.Bridge); !ok {
		add(Error, "network.bridge", "bridge %q not found on %s", s.Network.Bridge, inv.Node)
	}
}

// Plan validates the computed layout: addresses and ID/name conflicts.
func Plan(p *plan.Plan, inv *pve.Inventory, probes Probes) Findings {
	var f Findings
	add := func(sev Severity, field, msg string, args ...any) {
		f = append(f, Finding{sev, field, fmt.Sprintf(msg, args...)})
	}
	s := p.Spec
	if s.Network.VLAN < 0 || s.Network.VLAN > 4094 {
		add(Error, "network.vlan", "must be 1-4094 or 0 for untagged")
	}

	usedIPs := map[string]string{}
	for _, g := range inv.Guests {
		for _, ip := range g.IPs {
			usedIPs[ip] = fmt.Sprintf("%s %d (%s)", g.Type, g.VMID, g.Name)
		}
	}
	for _, b := range inv.Bridges {
		if b.Address != "" {
			usedIPs[b.Address] = "host bridge " + b.Name
		}
	}
	usedNames := map[string]int{}
	for _, g := range inv.Guests {
		usedNames[g.Name] = g.VMID
	}
	planned := map[string]string{}
	subnets := map[netip.Prefix]bool{}

	for _, n := range p.Nodes {
		field := "nodes." + n.Key
		subnet, ok := checkNodeNet(&f, field, n)
		if ok {
			subnets[subnet] = true
			for _, cidr := range []string{s.K3s.ClusterCIDR, s.K3s.ServiceCIDR} {
				if pc, err := netip.ParsePrefix(cidr); err == nil && pc.Overlaps(subnet) {
					add(Error, "k3s", "%s overlaps node subnet %s (%s)", cidr, subnet, n.Hostname)
				}
			}
		}
		if other, dup := planned[n.IP]; dup {
			add(Error, field, "IP %s is assigned to both %s and %s", n.IP, other, n.Hostname)
		}
		planned[n.IP] = n.Hostname
		if who, ok := usedIPs[n.IP]; ok {
			add(Error, field, "IP %s is already used by %s", n.IP, who)
		} else if probes.IPInUse != nil && probes.IPInUse(n.IP) {
			add(Error, field, "IP %s answers on the network (in use by an unknown device)", n.IP)
		}
		if g, ok := inv.Guest(n.CTID); ok {
			add(Error, field, "CT ID %d is already used by %s %q; pk3s never reuses existing guests", n.CTID, g.Type, g.Name)
		}
		if id, ok := usedNames[n.Hostname]; ok {
			add(Error, field, "hostname %s is already used by guest %d", n.Hostname, id)
		}
		if n.DataDir != "" && probes.DirNonEmpty != nil && probes.DirNonEmpty(n.DataDir) {
			add(Error, field, "data directory %s already exists and is not empty; pk3s will not reuse foreign data", n.DataDir)
		}
	}
	if len(subnets) > 1 {
		add(Warning, "node_network", "nodes are in %d different subnets on one bridge; they must be routable to each other (TCP 6443, 10250, UDP 8472)", len(subnets))
	}
	if probes.StateExists != nil && probes.StateExists(s.Name) {
		add(Error, "name", "pk3s state for cluster %q already exists; resume it or choose another name", s.Name)
	}
	if !p.Spec.Host.LoadKernelModules && len(p.MissingModules) > 0 {
		add(Warning, "host.load_kernel_modules", "modules not loaded on host: %s; k3s may fail", strings.Join(p.MissingModules, ", "))
	}
	return f
}

// checkNodeNet validates one node's address, prefix, gateway and DNS.
func checkNodeNet(f *Findings, field string, n plan.Node) (netip.Prefix, bool) {
	add := func(sev Severity, msg string, args ...any) {
		*f = append(*f, Finding{sev, field, fmt.Sprintf(msg, args...)})
	}
	a, err := netip.ParseAddr(n.IP)
	if err != nil || !a.Is4() {
		add(Error, "IP %q is not a valid IPv4 address", n.IP)
		return netip.Prefix{}, false
	}
	if n.Prefix < 8 || n.Prefix > 30 {
		add(Error, "prefix /%d must be between 8 and 30", n.Prefix)
		return netip.Prefix{}, false
	}
	subnet := netip.PrefixFrom(a, n.Prefix).Masked()
	if a == subnet.Addr() || a == lastAddr(subnet) {
		add(Error, "IP %s is the network or broadcast address of %s", a, subnet)
	}
	gw, err := netip.ParseAddr(n.Gateway)
	switch {
	case n.Gateway == "":
		add(Error, "gateway is required")
	case err != nil:
		add(Error, "gateway %q is not a valid address", n.Gateway)
	case !subnet.Contains(gw):
		add(Error, "gateway %s is not in %s", gw, subnet)
	case gw == a:
		add(Error, "IP %s equals the gateway", a)
	}
	if len(n.DNS) == 0 {
		add(Warning, "no DNS server set; the container inherits the host resolv.conf")
	}
	for _, d := range n.DNS {
		if _, err := netip.ParseAddr(d); err != nil {
			add(Error, "DNS server %q is not a valid address", d)
		}
	}
	return subnet, true
}

func lastAddr(p netip.Prefix) netip.Addr {
	a := p.Addr().As4()
	hostBits := 32 - p.Bits()
	for i := 3; i >= 0 && hostBits > 0; i-- {
		n := min(hostBits, 8)
		a[i] |= byte((1 << n) - 1)
		hostBits -= n
	}
	return netip.AddrFrom4(a)
}
