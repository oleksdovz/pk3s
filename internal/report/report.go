// Package report renders plans, findings and status as plain text. The
// same text is used by the CLI and inside TUI panels.
package report

import (
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/oleksdovz/pk3s/internal/app"
	"github.com/oleksdovz/pk3s/internal/plan"
	"github.com/oleksdovz/pk3s/internal/state"
	"github.com/oleksdovz/pk3s/internal/validate"
)

// Findings renders validation findings, errors first.
func Findings(fs validate.Findings) string {
	if len(fs) == 0 {
		return "No issues found.\n"
	}
	sorted := append(validate.Findings{}, fs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Severity > sorted[j].Severity })
	var b strings.Builder
	for _, f := range sorted {
		fmt.Fprintf(&b, "%-5s %s: %s\n", f.Severity, f.Field, f.Message)
	}
	return b.String()
}

// Plan renders the full deployment plan.
func Plan(p *plan.Plan) string {
	s := p.Spec
	var b strings.Builder
	fmt.Fprintf(&b, "Cluster      %s   (hostname prefix: %s)\n", s.Name, s.Prefix())
	fmt.Fprintf(&b, "k3s          %s (%s)\n", p.K3sVersion, p.Arch)
	fmt.Fprintf(&b, "Datastore    %s\n", p.Datastore)
	fmt.Fprintf(&b, "API endpoint %s  (first server; not a floating VIP)\n", p.ServerURL())
	fmt.Fprintf(&b, "Template     %s\n", s.Template.VolID())
	fmt.Fprintf(&b, "Network      bridge %s, default %s/%d via %s", s.Network.Bridge, s.Network.FirstIP, s.Network.Prefix, s.Network.Gateway)
	if s.Network.VLAN > 0 {
		fmt.Fprintf(&b, " VLAN %d", s.Network.VLAN)
	}
	fmt.Fprintf(&b, ", DNS %s\n", strings.Join(s.Network.DNS, " "))
	if len(s.K3s.Disable) > 0 {
		fmt.Fprintf(&b, "Disabled     %s\n", strings.Join(s.K3s.Disable, ", "))
	}
	if s.OnBoot {
		fmt.Fprintf(&b, "Autostart    start: main (order %d) first, wait %ds, then nodes (order %d) %ds apart; shutdown: nodes first, main last\n",
			s.Startup.MainOrder, s.Startup.MainDelaySec, s.Startup.NodeOrder, s.Startup.NodeDelaySec)
	} else {
		b.WriteString("Autostart    off\n")
	}
	fmt.Fprintf(&b, "Tags         %s\n\n", strings.Join(p.Tags, ";"))

	if p.Mode == plan.ModeUpdate {
		fmt.Fprintf(&b, "\nUPDATE of the running cluster (k3s %s -> %s)\nChanges, applied one at a time in this order:\n", p.FromVersion, p.K3sVersion)
		if len(p.Changes) == 0 {
			b.WriteString("  (none)\n")
		}
		for _, c := range p.Changes {
			fmt.Fprintf(&b, "  * %s\n", c)
		}
		b.WriteString("\nTarget layout (existing nodes keep their CT ID and address)\n")
	} else {
		b.WriteString("Containers\n")
	}
	tw := tabwriter.NewWriter(&b, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "  CTID\tHOSTNAME\tROLE\tADDRESS\tGATEWAY\tDNS\tCPU\tRAM\tDISK\tDATA DIR")
	for _, n := range p.Nodes {
		role := string(n.Role)
		if n.Bootstrap {
			role += " (init)"
		}
		if p.Mode == plan.ModeUpdate && !p.Existing[n.Key] {
			role += " NEW"
		}
		data := "-"
		if n.DataDir != "" {
			data = n.DataDir
		}
		fmt.Fprintf(tw, "  %d\t%s\t%s\t%s/%d\t%s\t%s\t%d\t%d MiB\t%d GiB on %s\t%s\n",
			n.CTID, n.Hostname, role, n.IP, n.Prefix, n.Gateway, strings.Join(n.DNS, ","), n.Resources.Cores, n.Resources.MemoryMiB, n.Resources.DiskGiB, s.RootfsStorage, data)
	}
	for _, n := range p.Removed {
		fmt.Fprintf(tw, "  %d\t%s\t%s REMOVE\t%s/%d\t%s\t%s\t%d\t%d MiB\t%d GiB\t%s\n",
			n.CTID, n.Hostname, n.Role, n.IP, n.Prefix, n.Gateway, strings.Join(n.DNS, ","), n.Resources.Cores, n.Resources.MemoryMiB, n.Resources.DiskGiB, orDash(n.DataDir))
	}
	tw.Flush()

	fmt.Fprintf(&b, "\nSecurity profile: %s — %s\n", p.Profile.ID, p.Profile.Status)
	fmt.Fprintf(&b, "  Risk: %s\n", p.Profile.Risk)
	fmt.Fprintf(&b, "  Features: %s\n", strings.Join(p.Profile.Features, ","))
	for _, st := range p.Profile.Settings {
		fmt.Fprintf(&b, "  + %s\n      %s\n", st.Line, st.Reason)
	}
	if len(p.Profile.KubeProxyArgs)+len(p.Profile.KubeletArgs) > 0 {
		fmt.Fprintf(&b, "  k3s args: kube-proxy %v kubelet %v\n", p.Profile.KubeProxyArgs, p.Profile.KubeletArgs)
	}

	b.WriteString("\nHost changes\n")
	for _, h := range p.HostChanges {
		fmt.Fprintf(&b, "  * %s\n      %s\n", h.Description, h.Path)
	}
	b.WriteString("\nSteps\n")
	for i, st := range p.Steps {
		fmt.Fprintf(&b, "  %2d. %s\n", i+1, st.Title)
	}
	return b.String()
}

// Status renders a status report.
func Status(r *app.StatusReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Cluster %s, k3s %s\n\n", r.Spec.Name, r.K3sVersion)
	tw := tabwriter.NewWriter(&b, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "CTID\tHOSTNAME\tROLE\tIP\tCONTAINER\tKUBERNETES\tVERSION")
	for _, n := range r.Nodes {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", n.Node.CTID, n.Node.Hostname, n.Node.Role, n.Node.IP, n.CTStatus, n.K8sReady, n.K8sVer)
	}
	tw.Flush()
	if r.K8sError != "" {
		fmt.Fprintf(&b, "\n%s\n", r.K8sError)
	}
	done, failed := 0, ""
	for _, st := range r.StepOrder {
		switch r.Steps[st.ID].Status {
		case state.StatusDone:
			done++
		case state.StatusFailed:
			failed = st.Title + ": " + r.Steps[st.ID].Error
		}
	}
	fmt.Fprintf(&b, "\nDeployment steps: %d/%d done\n", done, len(r.StepOrder))
	if failed != "" {
		fmt.Fprintf(&b, "Last failure: %s\n", failed)
	}
	if r.Kubeconfig != "" {
		fmt.Fprintf(&b, "Kubeconfig: %s\n", r.Kubeconfig)
	}
	return b.String()
}

// Summary renders the final summary after a successful deployment.
func Summary(p *plan.Plan, kubeconfig string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Cluster %q is up. k3s %s, %s.\n\n", p.Spec.Name, p.K3sVersion, p.Datastore)
	tw := tabwriter.NewWriter(&b, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "CTID\tHOSTNAME\tROLE\tIP\tCPU\tRAM\tDISK")
	for _, n := range p.Nodes {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%d\t%d MiB\t%d GiB\n", n.CTID, n.Hostname, n.Role, n.IP, n.Resources.Cores, n.Resources.MemoryMiB, n.Resources.DiskGiB)
	}
	tw.Flush()
	fmt.Fprintf(&b, `
Access
  on this host:           pct exec %[3]d -- kubectl get nodes
  shell on main1:         pct enter %[3]d     (kubectl works inside)
  kubeconfig (mode 0600): %[1]s
  on a workstation:       scp root@<proxmox-host>:%[1]s ~/.kube/%[2]s.yaml
                          KUBECONFIG=~/.kube/%[2]s.yaml kubectl get nodes
  API endpoint:           %[4]s
  (Do not export KUBECONFIG on the Proxmox host: pct enter would carry it
  into the containers.)

Secrets (join tokens) are stored in the same directory as secrets.json (mode 0600).
`, kubeconfig, p.Spec.Name, p.Nodes[0].CTID, p.ServerURL())
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
