// Command pk3s deploys k3s clusters into Proxmox VE LXC containers.
package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/mattn/go-isatty"

	"github.com/oleksdovz/pk3s/internal/app"
	"github.com/oleksdovz/pk3s/internal/engine"
	"github.com/oleksdovz/pk3s/internal/plan"
	"github.com/oleksdovz/pk3s/internal/remote"
	"github.com/oleksdovz/pk3s/internal/report"
	"github.com/oleksdovz/pk3s/internal/runner"
	"github.com/oleksdovz/pk3s/internal/spec"
	"github.com/oleksdovz/pk3s/internal/state"
	"github.com/oleksdovz/pk3s/internal/tui"
)

var version = "dev"

const usage = `pk3s - deploy k3s clusters into Proxmox VE LXC containers

Usage:
  pk3s                       start the interactive terminal UI
  pk3s init NAME [-o FILE]   scan this host and write an annotated starter spec
                             to NAME.yaml (or to stdout when redirected:
                             pk3s init NAME > cluster.yaml). For an existing
                             cluster, writes its deployed spec instead
  pk3s plan    -f FILE       validate FILE and show the deployment plan (no changes)
  pk3s apply   -f FILE       show the plan, ask for confirmation, deploy;
                             for an existing cluster: apply the changes in FILE
                             (scale agents, add servers, upgrade k3s, CPU/RAM/disk)
  pk3s resume  NAME          continue an interrupted or failed deployment
  pk3s status  NAME          show containers and Kubernetes node status
  pk3s list                  list clusters managed by pk3s
  pk3s destroy NAME          remove containers created by pk3s (asks first)
  pk3s kubeconfig NAME       print the cluster's admin kubeconfig (secret!)
  pk3s version

Remote mode (run from a workstation: macOS, Windows, Linux):
  pk3s --host root@pve [--ssh-port N] [--ssh-key FILE] [--remote-bin FILE] COMMAND ...
                             uploads pk3s to the Proxmox host (verified by SHA-256)
                             and runs COMMAND there; -f files are uploaded, init
                             output and redirected stdout stay local, e.g.
                             pk3s --host root@pve kubeconfig lab > ~/.kube/lab.yaml

Global flags:
  --root DIR                 state directory (default /var/lib/pk3s)
  --log-dir DIR              activity log directory (default /var/log/pk3s)
  --kubeconfig-dir DIR       where <name>-kubeconfig.yaml is written after every
                             successful create/update (default: current
                             directory; "-" disables)
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) (err error) {
	// Remote mode: --host [user@]proxmox runs pk3s on that host over SSH.
	if t, rest, ok := remoteTarget(args); ok {
		return runRemote(t, rest)
	}

	root, logDir, kubeDir := state.DefaultRoot, app.DefaultLogDir, "."
	// Extract --root / --log-dir anywhere in the arguments.
	var rest []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--root" && i+1 < len(args):
			root = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--root="):
			root = strings.TrimPrefix(args[i], "--root=")
		case args[i] == "--kubeconfig-dir" && i+1 < len(args):
			kubeDir = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--kubeconfig-dir="):
			kubeDir = strings.TrimPrefix(args[i], "--kubeconfig-dir=")
		case args[i] == "--log-dir" && i+1 < len(args):
			logDir = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--log-dir="):
			logDir = strings.TrimPrefix(args[i], "--log-dir=")
		default:
			rest = append(rest, args[i])
		}
	}
	cmd := "tui"
	if len(rest) > 0 {
		cmd, rest = rest[0], rest[1:]
	}
	switch cmd {
	case "version", "--version":
		fmt.Println("pk3s", version)
		return nil
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	}
	if os.Geteuid() != 0 {
		return errors.New("pk3s must run as root on a Proxmox VE host (from a workstation use: pk3s --host root@<proxmox> ...)")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer cancel()

	a := app.New(root)
	a.Version = version
	a.KubeconfigDir = kubeDir
	if err := a.OpenLog(logDir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: activity log disabled: %v\n", err)
	}
	defer a.Log.Close()
	host, _ := os.Hostname()
	a.Logf("pk3s %s start on %s: %s", version, host, strings.Join(append([]string{cmd}, rest...), " "))
	defer func() {
		if err != nil {
			a.Logf("pk3s %s failed: %v", cmd, err)
		} else {
			a.Logf("pk3s %s done", cmd)
		}
	}()
	switch cmd {
	case "tui":
		return tui.Run(ctx, a, version)
	case "init":
		return cmdInit(ctx, a, rest)
	case "plan":
		_, err := cmdPlan(ctx, a, rest)
		return err
	case "apply":
		return cmdApply(ctx, a, rest)
	case "resume":
		return cmdResume(ctx, a, rest)
	case "status":
		return cmdStatus(ctx, a, rest)
	case "list":
		return cmdList(root, rest)
	case "destroy":
		return cmdDestroy(ctx, a, rest)
	case "kubeconfig":
		return cmdKubeconfig(a, rest)
	default:
		fmt.Print(usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func cmdInit(ctx context.Context, a *app.App, args []string) error {
	name, out, force := "", "", false
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "-o" && i+1 < len(args):
			out = args[i+1]
			i++
		case strings.HasPrefix(arg, "-o="):
			out = strings.TrimPrefix(arg, "-o=")
		case arg == "--force" || arg == "-force":
			force = true
		case strings.HasPrefix(arg, "-"):
			return fmt.Errorf("unknown flag %s (usage: pk3s init NAME [-o FILE] [--force])", arg)
		default:
			if name != "" {
				return fmt.Errorf("unexpected argument %q (usage: pk3s init NAME [-o FILE] [--force])", arg)
			}
			name = arg
		}
	}
	if name == "" {
		return errors.New("cluster name required: pk3s init NAME [-o FILE] [--force]  (e.g. pk3s init lsb1-k3s > cluster.yaml)")
	}
	if !spec.ValidName(name) {
		return fmt.Errorf("invalid cluster name %q: 2-32 chars of lowercase letters, digits and '-', starting with a letter", name)
	}
	if out == "" {
		out = name + ".yaml"
		// `pk3s init NAME > file.yaml`: write the spec to the redirected stdout.
		if fi, err := os.Stdout.Stat(); err == nil && fi.Mode()&os.ModeCharDevice == 0 {
			out = "-"
		}
	}
	if _, err := os.Stat(out); out != "-" && err == nil && !force {
		return fmt.Errorf("%s already exists; use --force to overwrite or -o to choose another file", out)
	}
	res, err := a.InitSpec(ctx, name, version)
	if err != nil {
		return err
	}
	if out == "-" {
		fmt.Print(res.Content)
		if res.Existing {
			fmt.Fprintf(os.Stderr, "Cluster %q already exists: wrote its deployed spec.\n", name)
		}
		return nil
	}
	if err := os.WriteFile(out, []byte(res.Content), 0o600); err != nil {
		return err
	}
	if res.Existing {
		fmt.Printf("Cluster %q already exists: wrote its deployed spec to %s.\nEdit it and run: pk3s plan -f %s\n", name, out, out)
	} else {
		fmt.Printf("Scanned this host and wrote a starter spec to %s.\nSet network.first_ip, review the commented alternatives, then run: pk3s plan -f %s\n", out, out)
	}
	return nil
}

func specFile(args []string) (string, error) {
	fs := flag.NewFlagSet("pk3s", flag.ContinueOnError)
	file := fs.String("f", "", "cluster spec file")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if *file == "" {
		return "", errors.New("-f FILE is required")
	}
	return *file, nil
}

func cmdPlan(ctx context.Context, a *app.App, args []string) (*plan.Plan, error) {
	file, err := specFile(args)
	if err != nil {
		return nil, err
	}
	s, err := spec.Load(file)
	if err != nil {
		return nil, err
	}
	inv, err := a.Inventory(ctx)
	if err != nil {
		return nil, err
	}
	app.ApplyInventoryDefaults(&s, inv)
	prep, err := a.Prepare(ctx, s, inv)
	if err != nil {
		return nil, err
	}
	if prep.Plan != nil {
		fmt.Println(report.Plan(prep.Plan))
	}
	fmt.Println("Validation")
	fmt.Print(report.Findings(prep.Findings))
	if prep.Findings.HasErrors() {
		return nil, errors.New("validation failed; nothing was changed")
	}
	if prep.Plan.Mode == plan.ModeUpdate && len(prep.Plan.Changes) == 0 {
		return nil, nil
	}
	return prep.Plan, nil
}

func confirm(prompt, want string) bool {
	fmt.Print(prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(line) == want
}

func cmdApply(ctx context.Context, a *app.App, args []string) error {
	p, err := cmdPlan(ctx, a, args)
	if err != nil {
		return err
	}
	if p == nil {
		fmt.Println("Nothing to change.")
		return nil
	}
	prompt := "\nType 'yes' to create the resources above: "
	if p.Mode == plan.ModeUpdate {
		prompt = "\nType 'yes' to apply the changes above to the running cluster: "
	}
	if !confirm(prompt, "yes") {
		a.Logf("apply of %q declined at confirmation", p.Spec.Name)
		return errors.New("aborted; nothing was changed")
	}
	a.Logf("apply of %q confirmed (mode %s)", p.Spec.Name, p.Mode)
	return execute(ctx, a, p, false)
}

func cmdResume(ctx context.Context, a *app.App, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: pk3s resume NAME")
	}
	p, err := a.LoadForResume(ctx, args[0])
	if err != nil {
		return err
	}
	return execute(ctx, a, p, true)
}

func execute(ctx context.Context, a *app.App, p *plan.Plan, resume bool) error {
	sess, err := a.NewSession(p, resume, func(ev engine.Event) {
		fmt.Println(engine.FormatEvent(ev))
	})
	if err != nil {
		return err
	}
	defer sess.Close()

	// First Ctrl-C: finish the current step and stop. Second: cancel.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	go func() {
		<-sig
		fmt.Println("\nInterrupt: stopping after the current step (Ctrl-C again to abort it)...")
		sess.Engine.RequestStop()
		<-sig
		cancel()
	}()

	if err := sess.Run(runCtx); err != nil {
		fmt.Fprintln(os.Stderr, "\n"+err.Error())
		fmt.Fprintln(os.Stderr, "\n"+app.RecoveryHint(p.Spec.Name, a.Root))
		if a.Log != nil {
			fmt.Fprintln(os.Stderr, "Full activity log: "+a.Log.Path)
		}
		return errors.New("deployment incomplete")
	}
	fmt.Println()
	if p.Mode == plan.ModeUpdate {
		fmt.Printf("Cluster %q updated.\n\n", p.Spec.Name)
		if err := cmdStatus(ctx, a, []string{p.Spec.Name}); err != nil {
			return err
		}
	} else {
		fmt.Print(report.Summary(p, sess.Store.Path("kubeconfig")))
	}
	if sess.LocalKubeconfig != "" {
		fmt.Printf("\nKubeconfig copied to %s (mode 0600)\n  KUBECONFIG=%s kubectl get nodes\n", sess.LocalKubeconfig, sess.LocalKubeconfig)
	}
	return nil
}

func cmdStatus(ctx context.Context, a *app.App, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: pk3s status NAME")
	}
	r, err := a.Status(ctx, args[0])
	if err != nil {
		return err
	}
	fmt.Print(report.Status(r))
	return nil
}

func cmdDestroy(ctx context.Context, a *app.App, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: pk3s destroy NAME")
	}
	name := args[0]
	dp, err := a.PlanDestroy(ctx, name)
	if err != nil {
		return err
	}
	fmt.Printf("Containers created by pk3s for %q:\n", name)
	for _, c := range dp.Containers {
		status := "will be stopped and destroyed"
		if !c.Exists || !c.Owned {
			status = c.Reason
		}
		fmt.Printf("  CT %d: %s\n", c.CTID, status)
	}
	if len(dp.Kept) > 0 {
		fmt.Println("Kept (never deleted automatically; remove manually if no longer needed):")
		for _, r := range dp.Kept {
			fmt.Printf("  %s %s\n", r.Kind, r.ID)
		}
	}
	if dp.Blocked() {
		return errors.New("refusing to destroy: ownership check failed")
	}
	if !confirm(fmt.Sprintf("\nType the cluster name (%s) to destroy these containers: ", name), name) {
		return errors.New("aborted; nothing was changed")
	}
	return a.Destroy(ctx, dp, func(s string) { fmt.Println(s) })
}

// remoteTarget extracts the remote-mode flags; ok is false without --host.
func remoteTarget(args []string) (remote.Target, []string, bool) {
	var t remote.Target
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		val := func(name string) (string, bool) {
			if a == name && i+1 < len(args) {
				i++
				return args[i], true
			}
			if v, ok := strings.CutPrefix(a, name+"="); ok {
				return v, true
			}
			return "", false
		}
		if v, ok := val("--host"); ok {
			t.Host = v
		} else if v, ok := val("--ssh-port"); ok {
			t.Port = v
		} else if v, ok := val("--ssh-key"); ok {
			t.Identity = v
		} else if v, ok := val("--remote-bin"); ok {
			t.BinaryPath = v
		} else {
			rest = append(rest, a)
		}
	}
	return t, rest, t.Host != ""
}

func runRemote(t remote.Target, args []string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer cancel()
	c := remote.Client{T: t, R: runner.Exec{}}
	info, err := c.Probe(ctx)
	if err != nil {
		return err
	}
	if !info.PVE {
		return fmt.Errorf("%s is not a Proxmox VE host (pct not found)", t.Host)
	}
	if !info.Root {
		host := t.Host[strings.LastIndex(t.Host, "@")+1:]
		return fmt.Errorf("pk3s needs root on the Proxmox host: use --host root@%s", host)
	}
	bin, _, err := remote.Binary(info.Arch, t.BinaryPath)
	if err != nil {
		return err
	}
	path, err := c.Install(ctx, bin)
	if err != nil {
		return err
	}
	localDir := kubeconfigDir(args)
	p := remote.Rewrite(remote.StripFlag(args, "--kubeconfig-dir"))
	// The remote pk3s must not write kubeconfigs into its own working
	// directory; they are fetched into the local one afterwards.
	fetch := remote.FetchesKubeconfig(p.Args)
	since := info.Now
	p.Args = append([]string{"--kubeconfig-dir=-"}, p.Args...)
	for idx, local := range p.Uploads {
		rp, err := c.Upload(ctx, local)
		if err != nil {
			return err
		}
		p.Args[idx+1] = rp // shifted by --kubeconfig-dir
	}
	var out io.Writer = os.Stdout
	var buf bytes.Buffer
	if p.SaveTo != "" {
		out = &buf
	}
	tty := p.SaveTo == "" && isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stdout.Fd())
	code, err := remote.Exec(ctx, t, path, p.Args, out, tty)
	if err != nil {
		return err
	}
	if fetch {
		saved, ferr := c.FetchKubeconfigs(ctx, path, since, localDir)
		for _, f := range saved {
			fmt.Fprintf(os.Stderr, "Kubeconfig saved to %s (mode 0600)\n", f)
		}
		if ferr != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", ferr)
		}
	}
	if code != 0 {
		os.Exit(code)
	}
	if p.SaveTo != "" {
		if err := os.WriteFile(p.SaveTo, buf.Bytes(), 0o600); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Wrote %s (scanned on %s). Run: pk3s --host %s plan -f %s\n", p.SaveTo, t.Host, t.Host, p.SaveTo)
	}
	return nil
}

func cmdKubeconfig(a *app.App, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: pk3s kubeconfig NAME")
	}
	store := &state.Store{Root: a.Root, Cluster: args[0]}
	if !store.Exists() {
		return a.NoStateError(args[0])
	}
	data, err := os.ReadFile(store.Path("kubeconfig"))
	if err != nil {
		return fmt.Errorf("no kubeconfig for %q yet: %w", args[0], err)
	}
	a.Logf("kubeconfig of %q printed to stdout", args[0])
	_, err = os.Stdout.Write(data)
	return err
}

// cmdList prints managed clusters. --changed-since UNIX lists only clusters
// whose state changed since then (remote mode fetches their kubeconfigs).
func cmdList(root string, args []string) error {
	var since int64
	for i := 0; i < len(args); i++ {
		if args[i] == "--changed-since" && i+1 < len(args) {
			v, err := strconv.ParseInt(args[i+1], 10, 64)
			if err != nil {
				return fmt.Errorf("--changed-since: %w", err)
			}
			since = v
			i++
		}
	}
	names, err := state.List(root)
	if err != nil {
		return err
	}
	for _, n := range names {
		if since > 0 {
			// state.json is saved by every create/update/resume.
			fi, err := os.Stat((&state.Store{Root: root, Cluster: n}).Path("state.json"))
			if err != nil || fi.ModTime().Unix() < since {
				continue
			}
		}
		fmt.Println(n)
	}
	return nil
}

// kubeconfigDir returns the local --kubeconfig-dir value for remote mode.
func kubeconfigDir(args []string) string {
	for i, a := range args {
		if a == "--kubeconfig-dir" && i+1 < len(args) {
			return args[i+1]
		}
		if v, ok := strings.CutPrefix(a, "--kubeconfig-dir="); ok {
			return v
		}
	}
	return "."
}
