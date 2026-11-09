package plan

import (
	"fmt"
	"sort"
	"strings"

	"github.com/oleksdovz/pk3s/internal/k3s"
	"github.com/oleksdovz/pk3s/internal/pve"
	"github.com/oleksdovz/pk3s/internal/spec"
)

// NodeRecord is the persisted identity of a deployed node. Once a node
// exists its CT ID, address and data directory never change.
type NodeRecord struct {
	Key       string         `json:"key"`
	Hostname  string         `json:"hostname"`
	Role      k3s.Role       `json:"role"`
	CTID      int            `json:"ctid"`
	IP        string         `json:"ip"`
	Prefix    int            `json:"prefix"`
	Gateway   string         `json:"gateway"`
	DNS       []string       `json:"dns,omitempty"`
	DataDir   string         `json:"data_dir,omitempty"`
	SharedDir string         `json:"shared_dir,omitempty"`
	Resources spec.Resources `json:"resources"`
	Startup   string         `json:"startup"`
	OnBoot    bool           `json:"onboot"`
}

// Records returns the layout of all planned nodes.
func (p *Plan) Records() []NodeRecord {
	out := make([]NodeRecord, 0, len(p.Nodes))
	for _, n := range p.Nodes {
		out = append(out, p.record(n))
	}
	return out
}

func (p *Plan) record(n Node) NodeRecord {
	return NodeRecord{
		Key: n.Key, Hostname: n.Hostname, Role: n.Role, CTID: n.CTID, IP: n.IP,
		Prefix: n.Prefix, Gateway: n.Gateway, DNS: n.DNS, DataDir: n.DataDir, SharedDir: n.SharedDir,
		Resources: n.Resources, Startup: p.StartupFor(n), OnBoot: p.Spec.OnBoot,
	}
}

func nodeFromRecord(r NodeRecord) Node {
	return Node{
		Key: r.Key, Hostname: r.Hostname, Role: r.Role, CTID: r.CTID, IP: r.IP,
		Prefix: r.Prefix, Gateway: r.Gateway, DNS: r.DNS, DataDir: r.DataDir, SharedDir: r.SharedDir, Resources: r.Resources,
	}
}

// UpdateInput describes a change from the deployed state to a target spec.
type UpdateInput struct {
	Current     spec.ClusterSpec
	Layout      []NodeRecord
	FromVersion string
	Target      spec.ClusterSpec
	ToVersion   string
	OpID        string
}

// BuildUpdate computes the plan that moves a deployed cluster to the target
// spec. Changes are applied one step at a time in this order: container
// settings, k3s upgrade (servers first), node removal (highest CT ID
// first), new nodes (servers first), a readiness check, and finally the
// state ConfigMap.
func BuildUpdate(in UpdateInput, inv *pve.Inventory) (*Plan, error) {
	p, err := BuildLayout(in.Target, inv, in.ToVersion, in.Layout)
	if err != nil {
		return nil, err
	}
	p.Mode, p.OpID, p.FromVersion = ModeUpdate, in.OpID, in.FromVersion
	p.HostChanges = nil
	p.Steps = nil

	recs := map[string]NodeRecord{}
	for _, r := range in.Layout {
		recs[r.Key] = r
	}
	target := map[string]bool{}
	for _, n := range p.Nodes {
		target[n.Key] = true
	}
	for _, r := range in.Layout {
		if !target[r.Key] {
			p.Removed = append(p.Removed, nodeFromRecord(r))
		}
	}
	sort.Slice(p.Removed, func(i, j int) bool { return p.Removed[i].CTID > p.Removed[j].CTID })

	var newNodes []Node
	for _, n := range p.Nodes {
		if !p.Existing[n.Key] {
			newNodes = append(newNodes, n)
		}
	}
	upgrade := in.ToVersion != in.FromVersion

	add := func(kind StepKind, node int, host, title string) {
		id := in.OpID + "/" + string(kind)
		if host != "" {
			id += "/" + host
		}
		p.Steps = append(p.Steps, Step{ID: id, Kind: kind, Title: title, Node: node})
	}

	if len(newNodes) > 0 && !inv.HasTemplate(p.Spec.Template.VolID()) && p.Spec.Template.Download {
		add(StepTemplate, -1, "", "Download template "+p.Spec.Template.Name)
		p.HostChanges = append(p.HostChanges, HostChange{"Download LXC template " + p.Spec.Template.Name, p.Spec.Template.VolID()})
	}
	if upgrade || len(newNodes) > 0 {
		add(StepArtifact, -1, "", "Download and verify k3s "+p.K3sVersion)
		p.HostChanges = append(p.HostChanges, HostChange{"Cache k3s " + p.K3sVersion + " binary and checksum file", "/var/lib/pk3s/cache/k3s/" + p.K3sVersion})
	}

	// 1. Container settings of existing nodes.
	for _, n := range p.Nodes {
		r, ok := recs[n.Key]
		if !ok {
			continue
		}
		if d := settingsDiff(r, p.record(n)); d != "" {
			add(StepUpdateCT, n.Index, n.Hostname, fmt.Sprintf("Update %s (CT %d): %s", n.Hostname, n.CTID, d))
			p.Changes = append(p.Changes, fmt.Sprintf("change %s (CT %d): %s", n.Hostname, n.CTID, d))
		}
	}

	// 2. Rolling k3s upgrade of existing nodes: servers, then agents.
	if upgrade {
		p.Changes = append(p.Changes, fmt.Sprintf("upgrade k3s %s -> %s (servers one by one, then agents; agents are drained)", in.FromVersion, in.ToVersion))
		add(StepBackup, -1, "", "Take an etcd snapshot on "+p.Nodes[0].Hostname)
		for _, role := range []k3s.Role{k3s.RoleServer, k3s.RoleAgent} {
			for _, n := range p.Nodes {
				if n.Role == role && p.Existing[n.Key] {
					add(StepUpgradeNode, n.Index, n.Hostname, fmt.Sprintf("Upgrade %s (CT %d) to %s", n.Hostname, n.CTID, in.ToVersion))
				}
			}
		}
	}

	// 3. Remove nodes, highest CT ID first.
	for i, n := range p.Removed {
		add(StepRemoveNode, i, n.Hostname, fmt.Sprintf("Drain and remove %s (CT %d)", n.Hostname, n.CTID))
		msg := fmt.Sprintf("remove %s (CT %d, %s)", n.Hostname, n.CTID, n.IP)
		if n.DataDir != "" {
			msg += "; data directory " + n.DataDir + " is kept"
		}
		p.Changes = append(p.Changes, msg)
	}

	// 4. New nodes: provision in parallel, join servers one at a time,
	// agents in parallel.
	p.Steps = append(p.Steps, nodeSteps(p, newNodes, in.OpID+"/")...)
	sharedDirAnnounced := false
	for _, n := range newNodes {
		p.Changes = append(p.Changes, fmt.Sprintf("add %s %s (CT %d, %s)", n.Role, n.Hostname, n.CTID, n.IP))
		if n.DataDir != "" {
			p.HostChanges = append(p.HostChanges, HostChange{
				Description: fmt.Sprintf("Create data directory for %s (bind-mounted at %s)", n.Hostname, k3s.LocalStoragePath),
				Path:        n.DataDir,
			})
		}
		if n.SharedDir != "" && !sharedDirAnnounced {
			sharedDirAnnounced = true
			p.HostChanges = append(p.HostChanges, HostChange{
				Description: fmt.Sprintf("Create shared directory, bind-mounted at %s in every container", k3s.SharedStoragePath),
				Path:        n.SharedDir,
			})
		}
	}

	if len(p.Changes) > 0 {
		add(StepFinalize, -1, "", "Finalize: all nodes Ready, kubeconfig, state ConfigMap")
	}
	return p, nil
}

// settingsDiff describes container setting changes that can be applied to
// a running container (CPU, RAM, disk growth, boot order).
func settingsDiff(cur, want NodeRecord) string {
	var d []string
	if cur.Resources.Cores != want.Resources.Cores {
		d = append(d, fmt.Sprintf("cores %d -> %d", cur.Resources.Cores, want.Resources.Cores))
	}
	if cur.Resources.MemoryMiB != want.Resources.MemoryMiB {
		d = append(d, fmt.Sprintf("memory %d -> %d MiB", cur.Resources.MemoryMiB, want.Resources.MemoryMiB))
	}
	if cur.Resources.DiskGiB != want.Resources.DiskGiB {
		d = append(d, fmt.Sprintf("disk %d -> %d GiB", cur.Resources.DiskGiB, want.Resources.DiskGiB))
	}
	if cur.Startup != want.Startup {
		d = append(d, fmt.Sprintf("startup %s -> %s", cur.Startup, want.Startup))
	}
	if cur.OnBoot != want.OnBoot {
		d = append(d, fmt.Sprintf("onboot %v -> %v", cur.OnBoot, want.OnBoot))
	}
	return strings.Join(d, ", ")
}

// Record returns the record of a planned node (used after an update step
// changes container settings).
func (p *Plan) Record(n Node) NodeRecord { return p.record(n) }
