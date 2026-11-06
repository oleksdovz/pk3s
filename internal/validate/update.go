package validate

import (
	"fmt"
	"reflect"

	"github.com/oleksdovz/pk3s/internal/k3s"
	"github.com/oleksdovz/pk3s/internal/plan"
	"github.com/oleksdovz/pk3s/internal/spec"
)

// Update checks that a change to a deployed cluster is supported. It
// complements Spec (target spec) and Plan (conflicts of new nodes).
func Update(in plan.UpdateInput) Findings {
	var f Findings
	add := func(sev Severity, field, msg string, args ...any) {
		f = append(f, Finding{sev, field, fmt.Sprintf(msg, args...)})
	}
	cur, tgt := in.Current, in.Target

	if cur.Prefix() != tgt.Prefix() {
		add(Error, "hostname_prefix", "cannot be changed for an existing cluster (%q)", cur.Prefix())
	}

	// Servers.
	switch {
	case tgt.Servers < cur.Servers:
		add(Error, "servers", "removing servers (%d -> %d) is not supported yet", cur.Servers, tgt.Servers)
	case tgt.Servers > cur.Servers:
		add(Info, "servers", "%d new server(s) join embedded etcd one at a time", tgt.Servers-cur.Servers)
	}

	// Settings baked into existing nodes or the datastore.
	immutable := []struct {
		field string
		a, b  any
	}{
		{"security", cur.Security, tgt.Security},
		{"k3s.disable", cur.K3s.Disable, tgt.K3s.Disable},
		{"k3s.tls_sans", cur.K3s.TLSSANs, tgt.K3s.TLSSANs},
		{"k3s.cluster_cidr", cur.K3s.ClusterCIDR, tgt.K3s.ClusterCIDR},
		{"k3s.service_cidr", cur.K3s.ServiceCIDR, tgt.K3s.ServiceCIDR},
		{"persistent_data", cur.Persistent, tgt.Persistent},
	}
	for _, c := range immutable {
		if !reflect.DeepEqual(norm(c.a), norm(c.b)) {
			add(Error, c.field, "cannot be changed for an existing cluster; revert it to the deployed value")
		}
	}

	// Settings that only affect nodes created from now on.
	if cur.Template != tgt.Template || cur.RootfsStorage != tgt.RootfsStorage ||
		!reflect.DeepEqual(norm(cur.Network), norm(tgt.Network)) || !reflect.DeepEqual(norm(cur.OS), norm(tgt.OS)) || cur.Access != tgt.Access {
		add(Info, "network", "template, storage, network defaults, OS and access settings apply only to new nodes")
	}

	// Existing nodes keep their network.
	recs := map[string]plan.NodeRecord{}
	for _, r := range in.Layout {
		recs[r.Key] = r
	}
	for _, o := range tgt.NodeNetwork {
		r, ok := recs[o.Node]
		if !ok {
			continue
		}
		if (o.IP != "" && o.IP != r.IP) || (o.Prefix != 0 && o.Prefix != r.Prefix) ||
			(o.Gateway != "" && o.Gateway != r.Gateway) || (len(o.DNS) > 0 && !reflect.DeepEqual(o.DNS, r.DNS)) {
			add(Error, "node_network", "network of existing node %s cannot be changed (deployed: %s/%d via %s)", r.Hostname, r.IP, r.Prefix, r.Gateway)
		}
	}

	// Disks only grow.
	for _, r := range in.Layout {
		want := tgt.Agent.DiskGiB
		if r.Role == k3s.RoleServer {
			want = tgt.Server.DiskGiB
		}
		if want < r.Resources.DiskGiB {
			add(Error, "disk_gib", "%s: disk can only grow (%d -> %d GiB)", r.Hostname, r.Resources.DiskGiB, want)
		}
	}

	// Removing agents.
	if tgt.Agents < cur.Agents {
		add(Warning, "agents", "removing %d agent(s): pods are drained; local-path volumes on those nodes become unavailable (host data directories are kept)", cur.Agents-tgt.Agents)
	}

	// k3s version.
	if k3s.IsChannel(tgt.K3s.Version) {
		add(Info, "k3s.version", "channel %q does not upgrade an existing cluster; set an exact version (e.g. %s) to upgrade", tgt.K3s.Version, in.FromVersion)
	}
	from, ok1 := k3s.ParseVersion(in.FromVersion)
	to, ok2 := k3s.ParseVersion(in.ToVersion)
	if ok1 && ok2 {
		switch {
		case to.Compare(from) < 0:
			add(Error, "k3s.version", "downgrade %s -> %s is not supported", in.FromVersion, in.ToVersion)
		case to.Major != from.Major || to.Minor > from.Minor+1:
			add(Error, "k3s.version", "upgrade one minor version at a time (%s -> v%d.%d.x first)", in.FromVersion, from.Major, from.Minor+1)
		}
	}
	return f
}

// norm treats nil and empty slices/structs alike for comparison.
func norm(v any) any {
	switch x := v.(type) {
	case []string:
		if len(x) == 0 {
			return []string(nil)
		}
	case spec.Network:
		if len(x.DNS) == 0 {
			x.DNS = nil
		}
		return x
	case spec.OS:
		if len(x.ExtraPackages) == 0 {
			x.ExtraPackages = nil
		}
		return x
	}
	return v
}
