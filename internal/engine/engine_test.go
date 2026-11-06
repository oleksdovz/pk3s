package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oleksdovz/pk3s/internal/engine"
	"github.com/oleksdovz/pk3s/internal/k3s"
	"github.com/oleksdovz/pk3s/internal/plan"
	"github.com/oleksdovz/pk3s/internal/pve"
	"github.com/oleksdovz/pk3s/internal/runner"
	"github.com/oleksdovz/pk3s/internal/spec"
	"github.com/oleksdovz/pk3s/internal/state"
	"github.com/oleksdovz/pk3s/internal/testutil"
)

const (
	secureServer = "K10aaaabbbbccccdddd::server:0123456789abcdef"
	secureAgent  = "K10aaaabbbbccccdddd::node:fedcba9876543210"
)

// sim is a tiny Proxmox + k3s simulator implementing runner.Runner.
type sim struct {
	mu       sync.Mutex
	cts      map[int]*simCT
	calls    []runner.Cmd
	failOnce map[string]bool // command prefix -> fail the next call
	foreign  map[int]bool    // pre-existing CTs not owned by pk3s
	kubectl  []string        // kubectl invocations (without the binary)
	stateCM  string          // last applied pk3s-state ConfigMap
}

type simCT struct {
	conf        string
	running     bool
	k3s         bool
	nodeDeleted bool
	files       map[string]string
}

func newSim() *sim {
	return &sim{cts: map[int]*simCT{}, failOnce: map[string]bool{}, foreign: map[int]bool{}}
}

func (s *sim) Run(_ context.Context, c runner.Cmd) (runner.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, c)
	line := strings.Join(append([]string{c.Name}, c.Args...), " ")
	for p := range s.failOnce {
		if strings.HasPrefix(line, p) {
			delete(s.failOnce, p)
			return runner.Result{ExitCode: 1}, &runner.ExitError{Cmd: line, ExitCode: 1, Stderr: "simulated failure"}
		}
	}
	ok := func(out string) (runner.Result, error) { return runner.Result{Stdout: []byte(out)}, nil }
	fail := func(msg string) (runner.Result, error) {
		return runner.Result{ExitCode: 2}, &runner.ExitError{Cmd: line, ExitCode: 2, Stderr: msg}
	}
	a := c.Args
	switch c.Name {
	case "modprobe":
		return ok("")
	case "tee":
		id := ctFromConf(a[1])
		s.cts[id].conf += string(c.Stdin)
		return ok("")
	case "cat":
		if id := ctFromConf(a[0]); id > 0 {
			ct, exists := s.cts[id]
			if !exists {
				return fail("no such file")
			}
			return ok(ct.conf)
		}
		return fail("unexpected cat " + a[0])
	case "pct":
		id, _ := strconv.Atoi(a[1])
		ct := s.cts[id]
		switch a[0] {
		case "create":
			if ct != nil {
				return fail("CT already exists")
			}
			tags := ""
			for i, x := range a {
				if x == "--tags" {
					tags = a[i+1]
				}
			}
			disk := "30"
			for i, x := range a {
				if x == "--rootfs" {
					disk = a[i+1][strings.Index(a[i+1], ":")+1:]
				}
			}
			s.cts[id] = &simCT{conf: "arch: amd64\ntags: " + tags + "\nrootfs: x-data:vm-" + a[1] + "-disk-0,size=" + disk + "G\n", files: map[string]string{}}
			return ok("")
		case "status":
			if ct == nil {
				return fail("Configuration file 'nodes/hp/lxc/" + a[1] + ".conf' does not exist")
			}
			if ct.running {
				return ok("status: running")
			}
			return ok("status: stopped")
		case "start":
			ct.running = true
			return ok("")
		case "stop":
			ct.running = false
			return ok("")
		case "destroy":
			delete(s.cts, id)
			return ok("")
		case "set":
			for i := 2; i+1 < len(a); i += 2 {
				ct.conf += strings.TrimLeft(a[i], "-") + ": " + a[i+1] + "\n"
			}
			return ok("")
		case "resize":
			ct.conf = regexpSize.ReplaceAllString(ct.conf, "size="+a[3])
			return ok("")
		case "push":
			data, err := os.ReadFile(a[2])
			if err != nil {
				return fail(err.Error())
			}
			ct.files[a[3]] = string(data)
			return ok("")
		case "exec":
			return s.exec(id, ct, a[3:], string(c.Stdin))
		}
	}
	return fail("sim: unhandled " + line)
}

func (s *sim) exec(id int, ct *simCT, args []string, stdin string) (runner.Result, error) {
	ok := func(out string) (runner.Result, error) { return runner.Result{Stdout: []byte(out)}, nil }
	cmd := strings.Join(args, " ")
	// pct exec runs without /usr/local/bin in PATH (seen on PVE 9.2).
	if args[0] == "k3s" || args[0] == "kubectl" {
		return runner.Result{ExitCode: 127}, fmt.Errorf("lxc-attach: Failed to exec %q: not in PATH", args[0])
	}
	if args[0] == k3s.BinaryPath && len(args) > 1 && args[1] == "kubectl" && (len(args) < 3 || args[2] != "get") {
		s.kubectl = append(s.kubectl, strings.Join(args[2:], " "))
		if args[2] == "apply" {
			s.stateCM = stdin
		}
		if args[2] == "delete" && args[3] == "node" {
			for _, c := range s.cts {
				if strings.Contains(c.files[k3s.ConfigPath], "node-name: "+args[4]+"\n") {
					c.nodeDeleted = true
				}
			}
		}
		return ok("")
	}
	switch {
	case args[0] == "mv":
		ct.files[args[3]] = ct.files[args[2]]
		delete(ct.files, args[2])
		return ok("")
	case cmd == k3s.BinaryPath+" etcd-snapshot save --name "+args[len(args)-1]:
		s.kubectl = append(s.kubectl, "etcd-snapshot "+args[len(args)-1])
		return ok("")
	case strings.HasPrefix(cmd, "systemctl restart --no-block"):
		return ok("")
	case cmd == k3s.BinaryPath+" kubectl get configmap pk3s-state -n kube-system -o json":
		if s.stateCM == "" {
			return runner.Result{ExitCode: 1}, fmt.Errorf("NotFound")
		}
		return ok(s.stateCM)
	case cmd == "systemctl is-system-running":
		return ok("running")
	case cmd == "/bin/sh -s":
		if strings.Contains(stdin, "systemctl restart --no-block") {
			ct.k3s = true
		}
		return ok("pk3s: node prepared")
	case strings.HasPrefix(cmd, "systemctl is-active"):
		if ct.k3s {
			return ok("active")
		}
		return ok("inactive")
	case cmd == "/usr/local/bin/k3s kubectl get --raw /readyz":
		return ok("ok")
	case strings.HasPrefix(cmd, "/usr/local/bin/k3s kubectl get node "):
		return ok("node/" + args[4])
	case cmd == "cat "+k3s.ServerTokenPath:
		return ok(secureServer)
	case cmd == "cat "+k3s.AgentTokenPath:
		return ok(secureAgent)
	case cmd == "cat "+k3s.KubeconfigPath:
		return ok("server: https://127.0.0.1:6443\ncurrent-context: default\nclient-key-data: SECRETKEY")
	case cmd == "/usr/local/bin/k3s kubectl get nodes -o json":
		return ok(s.nodesJSON())
	}
	return runner.Result{ExitCode: 1}, fmt.Errorf("sim exec: unhandled %q in CT %d", cmd, id)
}

func (s *sim) nodesJSON() string {
	type item struct {
		Metadata struct {
			Name   string            `json:"name"`
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
		Status struct {
			Conditions []map[string]string `json:"conditions"`
			NodeInfo   map[string]string   `json:"nodeInfo"`
		} `json:"status"`
	}
	var items []item
	for _, ct := range s.cts {
		if !ct.k3s || ct.nodeDeleted {
			continue
		}
		var it item
		for _, l := range strings.Split(ct.files[k3s.ConfigPath], "\n") {
			if v, ok := strings.CutPrefix(l, "node-name: "); ok {
				it.Metadata.Name = v
			}
		}
		it.Status.Conditions = []map[string]string{{"type": "Ready", "status": "True"}}
		if strings.Contains(ct.files[k3s.ConfigPath], "agent-token:") { // only servers carry it
			it.Metadata.Labels = map[string]string{"node-role.kubernetes.io/control-plane": "true", "node-role.kubernetes.io/etcd": "true"}
		}
		it.Status.NodeInfo = map[string]string{"kubeletVersion": ct.files[k3s.BinaryPath]}
		items = append(items, it)
	}
	b, _ := json.Marshal(map[string]any{"items": items})
	return string(b)
}

func ctFromConf(path string) int {
	if !strings.HasPrefix(path, "/etc/pve/lxc/") {
		return 0
	}
	id, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(path, "/etc/pve/lxc/"), ".conf"))
	return id
}

var regexpSize = regexp.MustCompile(`size=\d+G`)

// fakeFetch writes one "binary" per version whose content is the version,
// so the simulator can report kubeletVersion from the pushed file.
type fakeFetch struct{ dir string }

func (f fakeFetch) EnsureBinary(_ context.Context, _, version, _ string) (k3s.Artifact, error) {
	p := filepath.Join(f.dir, "k3s-"+version)
	if err := os.WriteFile(p, []byte(version), 0o755); err != nil {
		return k3s.Artifact{}, err
	}
	return k3s.Artifact{Version: version, Arch: "amd64", Path: p, SHA256: "abc"}, nil
}

// memFS records host writes outside the state dir instead of touching /etc.
type memFS struct {
	engine.OSFS
	mu      sync.Mutex
	root    string
	written map[string]string
}

func (m *memFS) WriteFile(p string, d []byte, perm os.FileMode) error {
	if strings.HasPrefix(p, m.root) {
		return m.OSFS.WriteFile(p, d, perm)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.written[p] = string(d)
	return nil
}

func (m *memFS) MkdirAll(p string, perm os.FileMode) error {
	if strings.HasPrefix(p, m.root) {
		return os.MkdirAll(p, perm)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.written[p+"/"] = ""
	return nil
}

type harness struct {
	sim    *sim
	eng    *engine.Engine
	store  *state.Store
	events []engine.Event
	fs     *memFS
}

func newHarness(t *testing.T, mutate func(*spec.ClusterSpec)) *harness {
	t.Helper()
	s := spec.Default()
	s.Name = "lab"
	s.Servers, s.Agents = 3, 2
	s.Template = spec.Template{Storage: "local", Name: "debian-13-standard_13.1-2_amd64.tar.zst"}
	s.RootfsStorage = "x-data"
	s.Network = spec.Network{Bridge: "vmbr0", FirstIP: "192.168.0.51", Prefix: 24, Gateway: "192.168.0.1", DNS: []string{"192.168.0.2"}}
	s.Security.AcknowledgePrivilegedRisk = true
	s.Persistent = spec.PersistentData{Enabled: true, HostPath: "/data/k3s-storage"}
	if mutate != nil {
		mutate(&s)
	}
	p, err := plan.Build(s, testutil.HPInventory(t), "v1.37.2+k3s1")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	store := &state.Store{Root: root, Cluster: "lab"}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	bin := root
	h := &harness{sim: newSim(), store: store, fs: &memFS{root: root, written: map[string]string{}}}
	h.eng = h.newEngine(p, state.New("lab", p.K3sVersion), &state.Secrets{}, bin)
	return h
}

func (h *harness) newEngine(p *plan.Plan, st *state.State, sec *state.Secrets, bin string) *engine.Engine {
	return &engine.Engine{
		Plan: p, Store: h.store, State: st, Secrets: sec,
		R: h.sim, PCT: pve.PCT{R: h.sim, Sleep: func(time.Duration) {}}, Pveam: pve.Pveam{R: h.sim},
		Fetch: fakeFetch{bin}, FS: h.fs, Redactor: &runner.Redactor{},
		Notify:   func(ev engine.Event) { h.events = append(h.events, ev) },
		Sleep:    func(time.Duration) {},
		Timeouts: engine.Timeouts{CTReady: time.Second, ServerReady: time.Second, AgentReady: time.Second, ClusterReady: time.Second, Poll: time.Millisecond},
	}
}

func TestFullDeployment(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.eng.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.sim.cts) != 5 {
		t.Fatalf("expected 5 containers, got %d", len(h.sim.cts))
	}
	// LXC profile and bind mount applied.
	conf := h.sim.cts[5000].conf
	for _, want := range []string{"tags: pk3s;pk3s-lab", "lxc.apparmor.profile: unconfined", "lxc.mount.entry: /dev/kmsg", "mp0: /data/k3s-storage/lab/lab-main1,mp=/var/lib/rancher/k3s/storage"} {
		if !strings.Contains(conf, want) {
			t.Errorf("CT 5000 config missing %q:\n%s", want, conf)
		}
	}
	// Bootstrap uses cluster-init with the generated token; joiners use the
	// secure tokens; agents never receive the server token.
	boot := h.sim.cts[5000].files[k3s.ConfigPath]
	if !strings.Contains(boot, "cluster-init: true") || strings.Contains(boot, "server:") {
		t.Errorf("bootstrap config:\n%s", boot)
	}
	if s2 := h.sim.cts[5001].files[k3s.ConfigPath]; !strings.Contains(s2, "token: "+secureServer) {
		t.Errorf("joining server config:\n%s", s2)
	}
	agent := h.sim.cts[5003].files[k3s.ConfigPath]
	if !strings.Contains(agent, "token: "+secureAgent) || strings.Contains(agent, secureServer) || strings.Contains(agent, "agent-token") {
		t.Errorf("agent config must only contain the agent token:\n%s", agent)
	}
	// Host changes went through the FS abstraction.
	if !strings.Contains(h.fs.written["/etc/modules-load.d/pk3s.conf"], "vxlan") {
		t.Error("modules-load file not written")
	}
	// Secrets and kubeconfig stored with 0600.
	for _, f := range []string{"secrets.json", "kubeconfig", "state.json"} {
		fi, err := os.Stat(h.store.Path(f))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v mode %v", f, err, fi.Mode())
		}
	}
	kc, _ := os.ReadFile(h.store.Path("kubeconfig"))
	if !strings.Contains(string(kc), "https://192.168.0.51:6443") {
		t.Error("kubeconfig not rewritten to endpoint")
	}
	// Temporary config files are removed.
	if entries, _ := os.ReadDir(h.store.Path("tmp")); len(entries) != 0 {
		t.Errorf("tmp not cleaned: %v", entries)
	}
	// Ledger contains every container and data dir.
	st, _ := h.store.Load()
	for id := 5000; id <= 5004; id++ {
		if !st.HasResource(state.KindContainer, strconv.Itoa(id)) {
			t.Errorf("ledger missing CT %d", id)
		}
	}
}

func TestSecretsNeverLeak(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.eng.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	sec, _ := h.store.LoadSecrets()
	secrets := []string{sec.ServerToken, sec.AgentToken, secureServer, secureAgent, "SECRETKEY"}
	for _, c := range h.sim.calls {
		line := c.String()
		for _, s := range secrets {
			if strings.Contains(line, s) {
				t.Errorf("secret in command line: %s", line)
			}
			if strings.Contains(string(c.Stdin), s) {
				t.Errorf("secret passed via stdin of %s", line)
			}
		}
	}
	for _, ev := range h.events {
		for _, s := range secrets {
			if strings.Contains(ev.Message, s) {
				t.Errorf("secret in event: %s", ev.Message)
			}
		}
	}
	stateJSON, _ := os.ReadFile(h.store.Path("state.json"))
	for _, s := range secrets {
		if strings.Contains(string(stateJSON), s) {
			t.Error("secret in state.json")
		}
	}
}

func TestResumeAfterFailure(t *testing.T) {
	h := newHarness(t, nil)
	// Fail the first push into the first agent (CT 5003).
	h.sim.failOnce["pct push 5003"] = true
	err := h.eng.Run(context.Background())
	var se *engine.StepError
	if !errors.As(err, &se) || se.Step.ID != "join/lab-node1" {
		t.Fatalf("expected failure at join/lab-node1, got %v", err)
	}
	st, _ := h.store.Load()
	if st.Steps["join/lab-node1"].Status != state.StatusFailed || st.Steps["provision/lab-main1"].Status != state.StatusDone {
		t.Fatalf("state not recorded: %+v", st.Steps)
	}
	creates := countPrefix(h.sim.calls, "pct create")

	// Resume with state and secrets reloaded from disk, as `pk3s resume` does.
	sec, _ := h.store.LoadSecrets()
	h.eng = h.newEngine(h.eng.Plan, st, sec, h.eng.Fetch.(fakeFetch).dir)
	if err := h.eng.Run(context.Background()); err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	// All containers were provisioned (in parallel) before the failure, so
	// resume creates nothing and retries only the failed join.
	if got := countPrefix(h.sim.calls, "pct create"); got != creates {
		t.Errorf("resume must not create containers again: %d -> %d", creates, got)
	}
	if !h.sim.cts[5003].k3s {
		t.Error("lab-node1 should have joined on resume")
	}
	skipped := 0
	for _, ev := range h.events {
		if ev.Kind == engine.EventStepSkipped {
			skipped++
		}
	}
	if skipped == 0 {
		t.Error("expected completed steps to be skipped on resume")
	}
}

func TestRefusesForeignContainer(t *testing.T) {
	h := newHarness(t, nil)
	// A container that pk3s did not create already occupies CT 5000.
	h.sim.cts[5000] = &simCT{conf: "arch: amd64\ntags: backup\n", files: map[string]string{}}
	err := h.eng.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "refusing to touch it") {
		t.Fatalf("expected refusal, got %v", err)
	}
	if countPrefix(h.sim.calls, "pct stop 5000") > 0 || countPrefix(h.sim.calls, "tee -a /etc/pve/lxc/5000.conf") > 0 ||
		countPrefix(h.sim.calls, "pct start 5000") > 0 || countPrefix(h.sim.calls, "pct exec 5000") > 0 {
		t.Error("foreign container must not be modified")
	}
	if strings.Contains(h.sim.cts[5000].conf, "pk3s") {
		t.Error("foreign container config changed")
	}
}

func TestStopBetweenSteps(t *testing.T) {
	h := newHarness(t, nil)
	h.eng.Notify = func(ev engine.Event) {
		if ev.Kind == engine.EventStepDone && ev.StepID == "provision/lab-main1" {
			h.eng.RequestStop()
		}
	}
	if err := h.eng.Run(context.Background()); !errors.Is(err, engine.ErrStopped) {
		t.Fatalf("expected ErrStopped, got %v", err)
	}
	// The running provision group finishes; nothing after it starts.
	if len(h.sim.cts) != 5 {
		t.Errorf("all 5 CTs of the provision group should exist, got %d", len(h.sim.cts))
	}
	for id, ct := range h.sim.cts {
		if ct.k3s {
			t.Errorf("CT %d: k3s must not be installed after stop", id)
		}
	}
}

func TestPrepareScriptPackages(t *testing.T) {
	s := engine.PrepareScript(engine.PrepareOpts{NFS: true, Extra: []string{"htop"}})
	if !strings.Contains(s, "nfs-common") || !strings.Contains(s, "htop") || strings.Contains(s, "openssh-server") {
		t.Fatal(s)
	}
}

func countPrefix(calls []runner.Cmd, prefix string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(strings.Join(append([]string{c.Name}, c.Args...), " "), prefix) {
			n++
		}
	}
	return n
}

func TestAddServersOneToThree(t *testing.T) {
	h := newHarness(t, func(s *spec.ClusterSpec) { s.Servers, s.Agents = 1, 1 })
	if err := h.eng.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	p := h.update(t, func(s *spec.ClusterSpec) { s.Servers = 3 }, "")
	if len(p.Changes) != 2 {
		t.Fatalf("changes: %v", p.Changes)
	}
	// Existing node1 keeps CT 5001; new servers take the next free IDs.
	for _, id := range []int{5002, 5003} {
		ct := h.sim.cts[id]
		if ct == nil || !ct.k3s {
			t.Fatalf("server CT %d not created/joined", id)
		}
		cfg := ct.files[k3s.ConfigPath]
		if !strings.Contains(cfg, "server: https://192.168.0.51:6443") || !strings.Contains(cfg, "token: "+secureServer) || strings.Contains(cfg, "cluster-init") {
			t.Errorf("CT %d must join main1 with the server token:\n%s", id, cfg)
		}
	}
	if _, ok := h.sim.cts[5001]; !ok {
		t.Error("existing agent must stay")
	}
}
