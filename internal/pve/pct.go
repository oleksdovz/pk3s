package pve

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/oleksdovz/pk3s/internal/runner"
)

// CreateOpts are the arguments for `pct create`.
type CreateOpts struct {
	VMID         int
	Template     string // volid
	Hostname     string
	Cores        int
	MemoryMiB    int
	RootfsStore  string
	DiskGiB      int
	Bridge       string
	VLAN         int
	MTU          int
	IP           string
	Prefix       int
	Gateway      string
	DNS          []string
	SearchDomain string
	Unprivileged bool
	Features     []string
	Tags         []string
	Description  string
	OnBoot       bool
	Timezone     string
	SSHKeyFile   string
}

// Args renders the pct create argument list. It never contains secrets:
// no root password is set.
func (o CreateOpts) Args() []string {
	net := fmt.Sprintf("name=eth0,bridge=%s,ip=%s/%d,gw=%s,type=veth", o.Bridge, o.IP, o.Prefix, o.Gateway)
	if o.VLAN > 0 {
		net += fmt.Sprintf(",tag=%d", o.VLAN)
	}
	if o.MTU > 0 {
		net += fmt.Sprintf(",mtu=%d", o.MTU)
	}
	args := []string{
		"create", strconv.Itoa(o.VMID), o.Template,
		"--hostname", o.Hostname,
		"--cores", strconv.Itoa(o.Cores),
		"--memory", strconv.Itoa(o.MemoryMiB),
		"--swap", "0",
		"--rootfs", fmt.Sprintf("%s:%d", o.RootfsStore, o.DiskGiB),
		"--net0", net,
		"--unprivileged", boolArg(o.Unprivileged),
		"--onboot", boolArg(o.OnBoot),
		"--start", "0",
	}
	if len(o.DNS) > 0 {
		args = append(args, "--nameserver", strings.Join(o.DNS, " "))
	}
	if o.SearchDomain != "" {
		args = append(args, "--searchdomain", o.SearchDomain)
	}
	if len(o.Features) > 0 {
		args = append(args, "--features", strings.Join(o.Features, ","))
	}
	if len(o.Tags) > 0 {
		args = append(args, "--tags", strings.Join(o.Tags, ";"))
	}
	if o.Description != "" {
		args = append(args, "--description", o.Description)
	}
	if o.Timezone != "" {
		args = append(args, "--timezone", o.Timezone)
	}
	if o.SSHKeyFile != "" {
		args = append(args, "--ssh-public-keys", o.SSHKeyFile)
	}
	return args
}

func boolArg(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// PCT wraps container lifecycle commands.
type PCT struct {
	R runner.Runner
	// Sleep is used between polls; replaceable in tests.
	Sleep func(time.Duration)
}

func (p PCT) sleep(d time.Duration) {
	if p.Sleep != nil {
		p.Sleep(d)
		return
	}
	time.Sleep(d)
}

func (p PCT) run(ctx context.Context, args ...string) error {
	_, err := p.R.Run(ctx, runner.Cmd{Name: "pct", Args: args})
	return err
}

// Create runs pct create.
func (p PCT) Create(ctx context.Context, o CreateOpts) error {
	return p.run(ctx, o.Args()...)
}

// ConfigPath returns the container config path on the local node.
func ConfigPath(vmid int) string { return fmt.Sprintf("/etc/pve/lxc/%d.conf", vmid) }

// ReadConfig returns the raw container config file.
func (p PCT) ReadConfig(ctx context.Context, vmid int) (string, error) {
	res, err := p.R.Run(ctx, runner.Cmd{Name: "cat", Args: []string{ConfigPath(vmid)}})
	if err != nil {
		return "", err
	}
	return string(res.Stdout), nil
}

// MissingLines returns lines not yet present (exact match) in the main
// section of a container config.
func MissingLines(conf string, lines []string) []string {
	present := map[string]bool{}
	for _, l := range strings.Split(conf, "\n") {
		if strings.HasPrefix(l, "[") { // snapshot sections start here
			break
		}
		present[strings.TrimSpace(l)] = true
	}
	var missing []string
	for _, l := range lines {
		if !present[l] {
			missing = append(missing, l)
		}
	}
	return missing
}

// AppendConfig appends raw lxc.* lines that are not yet present. The
// container must be stopped and must not have snapshots.
func (p PCT) AppendConfig(ctx context.Context, vmid int, lines []string) error {
	conf, err := p.ReadConfig(ctx, vmid)
	if err != nil {
		return err
	}
	if strings.Contains(conf, "\n[") || strings.HasPrefix(conf, "[") {
		return fmt.Errorf("CT %d has snapshot sections; refusing to edit config", vmid)
	}
	missing := MissingLines(conf, lines)
	if len(missing) == 0 {
		return nil
	}
	data := strings.Join(missing, "\n") + "\n"
	if conf != "" && !strings.HasSuffix(conf, "\n") {
		data = "\n" + data
	}
	_, err = p.R.Run(ctx, runner.Cmd{Name: "tee", Args: []string{"-a", ConfigPath(vmid)}, Stdin: []byte(data)})
	return err
}

// SetMountPoint configures mpN as a bind mount.
func (p PCT) SetMountPoint(ctx context.Context, vmid, n int, hostPath, ctPath string) error {
	return p.run(ctx, "set", strconv.Itoa(vmid), fmt.Sprintf("-mp%d", n), fmt.Sprintf("%s,mp=%s", hostPath, ctPath))
}

// Status returns "running", "stopped" or an error if the CT does not exist.
func (p PCT) Status(ctx context.Context, vmid int) (string, error) {
	out, err := runner.Output(ctx, p.R, runner.Cmd{Name: "pct", Args: []string{"status", strconv.Itoa(vmid)}})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(strings.TrimPrefix(out, "status:")), nil
}

// Start starts a container if it is not running.
func (p PCT) Start(ctx context.Context, vmid int) error {
	st, err := p.Status(ctx, vmid)
	if err != nil {
		return err
	}
	if st == "running" {
		return nil
	}
	return p.run(ctx, "start", strconv.Itoa(vmid))
}

// Stop stops a container (no-op when stopped).
func (p PCT) Stop(ctx context.Context, vmid int) error {
	st, err := p.Status(ctx, vmid)
	if err != nil {
		return err
	}
	if st == "stopped" {
		return nil
	}
	return p.run(ctx, "stop", strconv.Itoa(vmid))
}

// Reboot reboots a running container.
func (p PCT) Reboot(ctx context.Context, vmid int) error {
	return p.run(ctx, "reboot", strconv.Itoa(vmid))
}

// Destroy removes a stopped container and its volumes.
func (p PCT) Destroy(ctx context.Context, vmid int) error {
	return p.run(ctx, "destroy", strconv.Itoa(vmid), "--purge", "1")
}

// Exec runs a command inside the container.
func (p PCT) Exec(ctx context.Context, vmid int, args ...string) (string, error) {
	all := append([]string{"exec", strconv.Itoa(vmid), "--"}, args...)
	return runner.Output(ctx, p.R, runner.Cmd{Name: "pct", Args: all})
}

// ExecSecret runs a command whose output is sensitive and must not be logged.
func (p PCT) ExecSecret(ctx context.Context, vmid int, args ...string) (string, error) {
	all := append([]string{"exec", strconv.Itoa(vmid), "--"}, args...)
	return runner.Output(ctx, p.R, runner.Cmd{Name: "pct", Args: all, SecretOutput: true})
}

// ExecScript runs a shell script inside the container, passing it on stdin
// so its content does not appear in the host process list.
func (p PCT) ExecScript(ctx context.Context, vmid int, script string) (string, error) {
	return runner.Output(ctx, p.R, runner.Cmd{
		Name:  "pct",
		Args:  []string{"exec", strconv.Itoa(vmid), "--", "/bin/sh", "-s"},
		Stdin: []byte(script),
	})
}

// Push copies a host file into the container.
func (p PCT) Push(ctx context.Context, vmid int, src, dst, perms string) error {
	return p.run(ctx, "push", strconv.Itoa(vmid), src, dst, "--perms", perms)
}

// WaitReady waits until the container accepts exec and systemd finished
// booting (running or degraded).
func (p PCT) WaitReady(ctx context.Context, vmid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for {
		out, err := p.Exec(ctx, vmid, "systemctl", "is-system-running")
		state := strings.TrimSpace(out)
		if err == nil || state == "degraded" {
			return nil
		}
		if state == "" {
			last = err
		} else {
			last = fmt.Errorf("systemd state %q", state)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("CT %d not ready after %s: %v", vmid, timeout, last)
		}
		p.sleep(2 * time.Second)
	}
}

// Pveam wraps template management.
type Pveam struct{ R runner.Runner }

// Available lists downloadable system templates (after `pveam update`).
func (a Pveam) Available(ctx context.Context) ([]string, error) {
	if _, err := a.R.Run(ctx, runner.Cmd{Name: "pveam", Args: []string{"update"}}); err != nil {
		return nil, err
	}
	out, err := runner.Output(ctx, a.R, runner.Cmd{Name: "pveam", Args: []string{"available", "--section", "system"}})
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 {
			names = append(names, f[1])
		}
	}
	return names, nil
}

// Download fetches a template into storage. pveam verifies the checksum
// published in the Proxmox appliance index.
func (a Pveam) Download(ctx context.Context, storage, name string) error {
	_, err := a.R.Run(ctx, runner.Cmd{Name: "pveam", Args: []string{"download", storage, name}})
	return err
}
