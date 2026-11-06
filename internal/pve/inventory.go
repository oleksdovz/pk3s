// Package pve reads Proxmox VE host inventory and wraps the pct/pveam
// operations pk3s needs. All commands go through runner.Runner.
package pve

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/oleksdovz/pk3s/internal/runner"
)

// Storage is a Proxmox storage as seen from one node.
type Storage struct {
	ID      string
	Type    string
	Content []string
	Avail   int64 // bytes
	Total   int64 // bytes
	Active  bool
	Shared  bool
}

// Has reports whether the storage supports a content type.
func (s Storage) Has(content string) bool {
	for _, c := range s.Content {
		if c == content {
			return true
		}
	}
	return false
}

// Bridge is a Linux bridge on the node.
type Bridge struct {
	Name    string
	CIDR    string
	Address string
	Gateway string
	Active  bool
}

// Prefix returns the prefix length of the bridge CIDR (0 if unknown).
func (b Bridge) Prefix() int {
	if i := strings.Index(b.CIDR, "/"); i >= 0 {
		if n, err := strconv.Atoi(b.CIDR[i+1:]); err == nil {
			return n
		}
	}
	return 0
}

// Template is an LXC template volume.
type Template struct {
	VolID string
	Size  int64
}

// Name returns the file name part of the volume ID.
func (t Template) Name() string {
	if i := strings.LastIndex(t.VolID, "/"); i >= 0 {
		return t.VolID[i+1:]
	}
	return t.VolID
}

// Guest is an existing VM or container anywhere in the cluster.
type Guest struct {
	VMID   int
	Name   string
	Type   string // lxc | qemu
	Node   string
	Status string
	Tags   []string
	// IPs are static addresses from the container network config (lxc only).
	IPs []string
}

// HasTag reports whether the guest carries a tag.
func (g Guest) HasTag(tag string) bool {
	for _, t := range g.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// Inventory is a snapshot of the host state relevant to planning.
type Inventory struct {
	Node          string
	Nodes         []string
	PVEVersion    string
	Arch          string // amd64 | arm64 | raw uname -m
	Kernel        string
	CgroupV2      bool
	Storages      []Storage
	Bridges       []Bridge
	Templates     []Template
	Guests        []Guest
	LoadedModules map[string]bool
	FirewallOn    bool
	HostTimezone  string
	HostDNS       []string
	HostSearch    string
}

// Storage returns a storage by ID.
func (inv *Inventory) Storage(id string) (Storage, bool) {
	for _, s := range inv.Storages {
		if s.ID == id {
			return s, true
		}
	}
	return Storage{}, false
}

// Bridge returns a bridge by name.
func (inv *Inventory) Bridge(name string) (Bridge, bool) {
	for _, b := range inv.Bridges {
		if b.Name == name {
			return b, true
		}
	}
	return Bridge{}, false
}

// Guest returns a guest by VMID.
func (inv *Inventory) Guest(id int) (Guest, bool) {
	for _, g := range inv.Guests {
		if g.VMID == id {
			return g, true
		}
	}
	return Guest{}, false
}

// HasTemplate reports whether volid exists.
func (inv *Inventory) HasTemplate(volid string) bool {
	for _, t := range inv.Templates {
		if t.VolID == volid {
			return true
		}
	}
	return false
}

// Client gathers inventory via pvesh and a few read-only shell commands.
type Client struct {
	R runner.Runner
}

func (c Client) pvesh(ctx context.Context, out any, path string, extra ...string) error {
	args := append([]string{"get", path, "--output-format", "json"}, extra...)
	res, err := c.R.Run(ctx, runner.Cmd{Name: "pvesh", Args: args})
	if err != nil {
		return err
	}
	if err := json.Unmarshal(res.Stdout, out); err != nil {
		return fmt.Errorf("pvesh get %s: decode: %w", path, err)
	}
	return nil
}

// flexInt decodes numbers that pvesh sometimes renders as strings.
type flexInt int64

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	*f = flexInt(n)
	return nil
}

// Collect reads the full inventory for the local node.
func (c Client) Collect(ctx context.Context) (*Inventory, error) {
	inv := &Inventory{LoadedModules: map[string]bool{}}

	host, err := runner.Output(ctx, c.R, runner.Cmd{Name: "hostname", Args: []string{"-s"}})
	if err != nil {
		return nil, fmt.Errorf("detect node name: %w", err)
	}
	inv.Node = host

	var nodes []struct {
		Node   string `json:"node"`
		Status string `json:"status"`
	}
	if err := c.pvesh(ctx, &nodes, "/nodes"); err != nil {
		return nil, fmt.Errorf("list nodes (is this a Proxmox VE host?): %w", err)
	}
	for _, n := range nodes {
		inv.Nodes = append(inv.Nodes, n.Node)
	}
	sort.Strings(inv.Nodes)

	var ver struct {
		Version string `json:"version"`
		Release string `json:"release"`
	}
	if err := c.pvesh(ctx, &ver, "/version"); err != nil {
		return nil, err
	}
	inv.PVEVersion = ver.Version

	if arch, err := runner.Output(ctx, c.R, runner.Cmd{Name: "uname", Args: []string{"-m"}}); err == nil {
		inv.Arch = NormalizeArch(arch)
	}
	if k, err := runner.Output(ctx, c.R, runner.Cmd{Name: "uname", Args: []string{"-r"}}); err == nil {
		inv.Kernel = k
	}
	if fs, err := runner.Output(ctx, c.R, runner.Cmd{Name: "stat", Args: []string{"-fc", "%T", "/sys/fs/cgroup"}}); err == nil {
		inv.CgroupV2 = fs == "cgroup2fs"
	}

	if err := c.collectStorage(ctx, inv); err != nil {
		return nil, err
	}
	if err := c.collectBridges(ctx, inv); err != nil {
		return nil, err
	}
	if err := c.collectGuests(ctx, inv); err != nil {
		return nil, err
	}
	c.collectModules(ctx, inv)

	if out, err := runner.Output(ctx, c.R, runner.Cmd{Name: "pve-firewall", Args: []string{"status"}}); err == nil {
		inv.FirewallOn = strings.HasPrefix(strings.TrimPrefix(out, "Status: "), "enabled")
	}
	var dns struct {
		DNS1   string `json:"dns1"`
		DNS2   string `json:"dns2"`
		DNS3   string `json:"dns3"`
		Search string `json:"search"`
	}
	if err := c.pvesh(ctx, &dns, "/nodes/"+inv.Node+"/dns"); err == nil {
		for _, d := range []string{dns.DNS1, dns.DNS2, dns.DNS3} {
			if d != "" {
				inv.HostDNS = append(inv.HostDNS, d)
			}
		}
		inv.HostSearch = dns.Search
	}
	if tz, err := runner.Output(ctx, c.R, runner.Cmd{Name: "cat", Args: []string{"/etc/timezone"}}); err == nil {
		inv.HostTimezone = tz
	}
	return inv, nil
}

// NormalizeArch maps uname -m to Go/k3s architecture names.
func NormalizeArch(m string) string {
	switch strings.TrimSpace(m) {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	default:
		return strings.TrimSpace(m)
	}
}

func (c Client) collectStorage(ctx context.Context, inv *Inventory) error {
	var raw []struct {
		Storage string   `json:"storage"`
		Type    string   `json:"type"`
		Content string   `json:"content"`
		Avail   flexInt  `json:"avail"`
		Total   flexInt  `json:"total"`
		Active  flexInt  `json:"active"`
		Enabled *flexInt `json:"enabled"`
		Shared  flexInt  `json:"shared"`
	}
	if err := c.pvesh(ctx, &raw, "/nodes/"+inv.Node+"/storage"); err != nil {
		return fmt.Errorf("list storage: %w", err)
	}
	for _, s := range raw {
		if s.Enabled != nil && *s.Enabled == 0 {
			continue
		}
		st := Storage{
			ID: s.Storage, Type: s.Type, Avail: int64(s.Avail), Total: int64(s.Total),
			Active: s.Active == 1, Shared: s.Shared == 1,
		}
		for _, ct := range strings.Split(s.Content, ",") {
			if ct = strings.TrimSpace(ct); ct != "" {
				st.Content = append(st.Content, ct)
			}
		}
		inv.Storages = append(inv.Storages, st)
		if st.Active && st.Has("vztmpl") {
			var tmpl []struct {
				VolID string  `json:"volid"`
				Size  flexInt `json:"size"`
			}
			if err := c.pvesh(ctx, &tmpl, "/nodes/"+inv.Node+"/storage/"+st.ID+"/content", "--content", "vztmpl"); err == nil {
				for _, t := range tmpl {
					inv.Templates = append(inv.Templates, Template{VolID: t.VolID, Size: int64(t.Size)})
				}
			}
		}
	}
	sort.Slice(inv.Storages, func(i, j int) bool { return inv.Storages[i].ID < inv.Storages[j].ID })
	sort.Slice(inv.Templates, func(i, j int) bool { return inv.Templates[i].VolID < inv.Templates[j].VolID })
	return nil
}

func (c Client) collectBridges(ctx context.Context, inv *Inventory) error {
	var raw []struct {
		Iface   string  `json:"iface"`
		Type    string  `json:"type"`
		CIDR    string  `json:"cidr"`
		Address string  `json:"address"`
		Gateway string  `json:"gateway"`
		Active  flexInt `json:"active"`
	}
	if err := c.pvesh(ctx, &raw, "/nodes/"+inv.Node+"/network"); err != nil {
		return fmt.Errorf("list network: %w", err)
	}
	for _, n := range raw {
		if n.Type != "bridge" && n.Type != "OVSBridge" {
			continue
		}
		inv.Bridges = append(inv.Bridges, Bridge{Name: n.Iface, CIDR: n.CIDR, Address: n.Address, Gateway: n.Gateway, Active: n.Active == 1})
	}
	sort.Slice(inv.Bridges, func(i, j int) bool { return inv.Bridges[i].Name < inv.Bridges[j].Name })
	return nil
}

func (c Client) collectGuests(ctx context.Context, inv *Inventory) error {
	var raw []struct {
		VMID   flexInt `json:"vmid"`
		Name   string  `json:"name"`
		Type   string  `json:"type"`
		Node   string  `json:"node"`
		Status string  `json:"status"`
		Tags   string  `json:"tags"`
	}
	if err := c.pvesh(ctx, &raw, "/cluster/resources", "--type", "vm"); err != nil {
		return fmt.Errorf("list guests: %w", err)
	}
	for _, g := range raw {
		guest := Guest{VMID: int(g.VMID), Name: g.Name, Type: g.Type, Node: g.Node, Status: g.Status, Tags: SplitTags(g.Tags)}
		if g.Type == "lxc" {
			var cfg map[string]any
			path := fmt.Sprintf("/nodes/%s/lxc/%d/config", g.Node, guest.VMID)
			if err := c.pvesh(ctx, &cfg, path); err == nil {
				guest.IPs = staticIPs(cfg)
			}
		}
		inv.Guests = append(inv.Guests, guest)
	}
	sort.Slice(inv.Guests, func(i, j int) bool { return inv.Guests[i].VMID < inv.Guests[j].VMID })
	return nil
}

// SplitTags splits a Proxmox tag string (separated by ';', ',' or space).
func SplitTags(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == ',' || r == ' ' })
}

// staticIPs extracts ip= values from netN entries of an LXC config.
func staticIPs(cfg map[string]any) []string {
	var ips []string
	for k, v := range cfg {
		if !strings.HasPrefix(k, "net") {
			continue
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		for _, kv := range strings.Split(s, ",") {
			if ip, ok := strings.CutPrefix(kv, "ip="); ok && ip != "dhcp" && ip != "manual" {
				if i := strings.Index(ip, "/"); i >= 0 {
					ip = ip[:i]
				}
				ips = append(ips, ip)
			}
		}
	}
	sort.Strings(ips)
	return ips
}

// collectModules reads loaded and built-in kernel modules.
func (c Client) collectModules(ctx context.Context, inv *Inventory) {
	if out, err := runner.Output(ctx, c.R, runner.Cmd{Name: "cat", Args: []string{"/proc/modules"}}); err == nil {
		for _, line := range strings.Split(out, "\n") {
			if f := strings.Fields(line); len(f) > 0 {
				inv.LoadedModules[f[0]] = true
			}
		}
	}
	if inv.Kernel == "" {
		return
	}
	if out, err := runner.Output(ctx, c.R, runner.Cmd{Name: "cat", Args: []string{"/lib/modules/" + inv.Kernel + "/modules.builtin"}}); err == nil {
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			base := line[strings.LastIndex(line, "/")+1:]
			base = strings.TrimSuffix(base, ".ko")
			inv.LoadedModules[strings.ReplaceAll(base, "-", "_")] = true
		}
	}
}
