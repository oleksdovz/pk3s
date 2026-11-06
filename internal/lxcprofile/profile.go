// Package lxcprofile defines the LXC settings pk3s applies to containers,
// with a human-readable justification for every setting so the plan can
// show exactly how host isolation is weakened.
package lxcprofile

import (
	"fmt"

	"github.com/oleksdovz/pk3s/internal/spec"
)

// Setting is one raw lxc.* config line plus why it is needed.
type Setting struct {
	Line   string
	Reason string
}

// Profile is the complete container security configuration.
type Profile struct {
	ID           string
	Unprivileged bool
	Features     []string
	Settings     []Setting
	// KubeletArgs/KubeProxyArgs are k3s flags the profile requires.
	KubeletArgs   []string
	KubeProxyArgs []string
	Status        string // "community-tested" | "experimental"
	Risk          string
}

// Lines returns the raw config lines.
func (p Profile) Lines() []string {
	out := make([]string, len(p.Settings))
	for i, s := range p.Settings {
		out[i] = s.Line
	}
	return out
}

// kube-proxy tries to raise nf_conntrack_max, which is a host-global sysctl
// and cannot be written from a container network namespace. Setting the
// per-core value to 0 makes kube-proxy leave it alone.
var kubeProxyConntrack = "conntrack-max-per-core=0"

// For builds the profile for a spec.
func For(s spec.Security) (Profile, error) {
	switch s.Profile {
	case spec.ProfilePrivileged:
		return privileged(s), nil
	case spec.ProfileUnprivileged:
		return unprivileged(s), nil
	default:
		return Profile{}, fmt.Errorf("unknown security profile %q", s.Profile)
	}
}

func privileged(s spec.Security) Profile {
	p := Profile{
		ID:           spec.ProfilePrivileged,
		Unprivileged: false,
		Features:     []string{"nesting=1", "keyctl=1"},
		Status:       "community-tested (not supported by k3s or Proxmox upstream)",
		Risk: "Privileged container with AppArmor disabled, no dropped capabilities and access to all devices. " +
			"Container root is host root: a process escaping a pod or the container can fully control the Proxmox host. " +
			"Use only on hosts dedicated to trusted workloads.",
		Settings: []Setting{
			{"lxc.apparmor.profile: unconfined", "containerd/runc must mount proc, sysfs and overlay filesystems for pods; the default LXC AppArmor profile denies these mounts."},
			{"lxc.cap.drop:", "Proxmox drops capabilities (e.g. sys_module, sys_rawio, sys_time) by default; kubelet/containerd expect a full capability set to hand out to pods."},
			{"lxc.cgroup2.devices.allow: a", "Allows containerd to create device nodes for pods; without it many workloads fail with 'operation not permitted' on /dev."},
			{"lxc.mount.auto: proc:rw sys:rw cgroup:rw", "kubelet and kube-proxy write sysctls and manage cgroups; LXC mounts these read-only or mixed by default."},
			{"lxc.mount.entry: /dev/kmsg dev/kmsg none bind,optional,create=file", "kubelet's OOM watcher reads /dev/kmsg and refuses to start without it."},
		},
		KubeProxyArgs: []string{kubeProxyConntrack},
	}
	if s.TunDevice {
		p.Settings = append(p.Settings,
			Setting{"lxc.cgroup2.devices.allow: c 10:200 rwm", "Allows the TUN character device (major 10, minor 200)."},
			Setting{"lxc.mount.entry: /dev/net/tun dev/net/tun none bind,optional,create=file", "Exposes /dev/net/tun for VPN/WireGuard workloads (optional)."},
		)
	}
	if s.Fuse {
		p.Features = append(p.Features, "fuse=1")
	}
	return p
}

func unprivileged(s spec.Security) Profile {
	p := Profile{
		ID:           spec.ProfileUnprivileged,
		Unprivileged: true,
		Features:     []string{"nesting=1", "keyctl=1"},
		Status:       "EXPERIMENTAL: not documented by k3s; relies on the alpha KubeletInUserNamespace feature gate",
		Risk: "Container root is mapped to an unprivileged host UID, which keeps host isolation. " +
			"k3s may fail to start or some workloads may not work; expect to debug.",
		Settings: []Setting{
			{"lxc.mount.entry: /dev/kmsg dev/kmsg none bind,optional,create=file", "kubelet reads /dev/kmsg; may be unreadable for a mapped UID (kernel-dependent)."},
		},
		KubeletArgs:   []string{"feature-gates=KubeletInUserNamespace=true"},
		KubeProxyArgs: []string{kubeProxyConntrack},
	}
	if s.Fuse {
		p.Features = append(p.Features, "fuse=1")
	}
	return p
}

// HostModules are kernel modules k3s needs; containers share the host
// kernel and cannot load modules themselves.
var HostModules = []string{
	"overlay",      // containerd overlayfs snapshotter
	"br_netfilter", // bridged pod traffic through iptables
	"nf_conntrack", // kube-proxy / NAT
	"vxlan",        // flannel default backend
	"ip_tables",
	"iptable_nat",
	"iptable_filter",
	"xt_conntrack",
	"xt_comment",
	"xt_mark",
	"xt_MASQUERADE",
	"nf_nat",
	"nf_tables",
}

// NFSModules are needed when containers mount NFS volumes.
var NFSModules = []string{"nfs", "nfsv4"}

// ModulesFor returns the host modules required by a spec.
func ModulesFor(nfs bool) []string {
	mods := append([]string{}, HostModules...)
	if nfs {
		mods = append(mods, NFSModules...)
	}
	return mods
}

// ModulesLoadFile is where pk3s persists module loading on the host.
const ModulesLoadFile = "/etc/modules-load.d/pk3s.conf"

// MissingModules returns modules not present in loaded.
func MissingModules(required []string, loaded map[string]bool) []string {
	var m []string
	for _, mod := range required {
		if !loaded[mod] {
			m = append(m, mod)
		}
	}
	return m
}
