// Command pk3s deploys k3s clusters into Proxmox VE LXC containers.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/oleksdovz/pk3s/internal/app"
	"github.com/oleksdovz/pk3s/internal/engine"
	"github.com/oleksdovz/pk3s/internal/plan"
	"github.com/oleksdovz/pk3s/internal/report"
	"github.com/oleksdovz/pk3s/internal/spec"
	"github.com/oleksdovz/pk3s/internal/state"
	"github.com/oleksdovz/pk3s/internal/tui"
)

var version = "dev"

const usage = `pk3s - deploy k3s clusters into Proxmox VE LXC containers

Usage:
  pk3s                       start the interactive terminal UI
  pk3s init                  print a starter cluster.yaml for this host
  pk3s plan    -f FILE       validate FILE and show the deployment plan (no changes)
  pk3s apply   -f FILE       show the plan, ask for confirmation, deploy
  pk3s resume  NAME          continue an interrupted or failed deployment
  pk3s status  NAME          show containers and Kubernetes node status
  pk3s list                  list clusters managed by pk3s
  pk3s destroy NAME          remove containers created by pk3s (asks first)
  pk3s version

Global flags:
  --root DIR                 state directory (default /var/lib/pk3s)
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	root := state.DefaultRoot
	// Extract --root anywhere in the arguments.
	var rest []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--root" && i+1 < len(args):
			root = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--root="):
			root = strings.TrimPrefix(args[i], "--root=")
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
		return errors.New("pk3s must run as root on a Proxmox VE host")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer cancel()

	a := app.New(root, nil)
	switch cmd {
	case "tui":
		return tui.Run(ctx, a, version)
	case "init":
		return cmdInit(ctx, a)
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
		names, err := state.List(root)
		if err != nil {
			return err
		}
		for _, n := range names {
			fmt.Println(n)
		}
		return nil
	case "destroy":
		return cmdDestroy(ctx, a, rest)
	default:
		fmt.Print(usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func cmdInit(ctx context.Context, a *app.App) error {
	inv, err := a.Inventory(ctx)
	if err != nil {
		return err
	}
	s := spec.Default()
	app.ApplyInventoryDefaults(&s, inv)
	data, err := spec.Marshal(s)
	if err != nil {
		return err
	}
	fmt.Print("# pk3s cluster spec. Fill in network.first_ip, review everything, then:\n#   pk3s plan -f cluster.yaml\n")
	fmt.Print(string(data))
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
	if !confirm("\nType 'yes' to create the resources above: ", "yes") {
		return errors.New("aborted; nothing was changed")
	}
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

	if err := sess.Engine.Run(runCtx); err != nil {
		fmt.Fprintln(os.Stderr, "\n"+err.Error())
		fmt.Fprintln(os.Stderr, "\n"+app.RecoveryHint(p.Spec.Name, a.Root))
		return errors.New("deployment incomplete")
	}
	fmt.Println()
	fmt.Print(report.Summary(p, sess.Store.Path("kubeconfig")))
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
