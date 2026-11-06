package pve_test

import (
	"context"
	"strings"
	"testing"
	"time"

	. "github.com/oleksdovz/pk3s/internal/pve"
	"github.com/oleksdovz/pk3s/internal/runner"
	"github.com/oleksdovz/pk3s/internal/testutil"
)

func TestCollectFromRealHost(t *testing.T) {
	inv, err := Client{R: testutil.HPFake(t)}.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if inv.Node != "hp" || inv.PVEVersion != "9.2.26" || inv.Arch != "amd64" || !inv.CgroupV2 {
		t.Fatalf("host facts wrong: %+v", inv)
	}
	if inv.FirewallOn {
		t.Error("firewall reported on, fixture says disabled")
	}
	// Disabled NFS storage must be skipped.
	if _, ok := inv.Storage("NFS"); ok {
		t.Error("disabled storage NFS should be ignored")
	}
	xd, ok := inv.Storage("x-data")
	if !ok || xd.Type != "lvmthin" || !xd.Has("rootdir") || xd.Avail != 978208298946 {
		t.Errorf("x-data storage wrong: %+v", xd)
	}
	br, ok := inv.Bridge("vmbr0")
	if !ok || br.Gateway != "192.168.0.1" || br.Prefix() != 24 || br.Address != "192.168.0.5" {
		t.Errorf("vmbr0 wrong: %+v", br)
	}
	if !inv.HasTemplate("local:vztmpl/debian-13-standard_13.1-2_amd64.tar.zst") {
		t.Error("expected debian-13 template on local")
	}
	if !inv.HasTemplate("DATA:vztmpl/ubuntu-24.04-standard_24.04-2_amd64.tar.zst") {
		t.Error("expected ubuntu template on DATA")
	}
	g, ok := inv.Guest(9000)
	if !ok || g.Type != "lxc" || len(g.IPs) != 1 || g.IPs[0] != "192.168.0.6" || !g.HasTag("backup") {
		t.Errorf("guest 9000 wrong: %+v", g)
	}
	if len(inv.HostDNS) != 1 || inv.HostDNS[0] != "192.168.0.2" || inv.HostSearch != "lan" {
		t.Errorf("dns wrong: %v %q", inv.HostDNS, inv.HostSearch)
	}
	// br_netfilter is built into this kernel, nf_conntrack is loaded,
	// overlay and vxlan are not.
	if !inv.LoadedModules["br_netfilter"] || !inv.LoadedModules["nf_conntrack"] {
		t.Error("expected br_netfilter (builtin) and nf_conntrack (loaded)")
	}
	if inv.LoadedModules["vxlan"] {
		t.Error("vxlan should not be loaded in fixture")
	}
}

func TestCreateOptsArgsHaveNoPasswordAndTags(t *testing.T) {
	o := CreateOpts{
		VMID: 5000, Template: "local:vztmpl/debian.tar.zst", Hostname: "k-s1", Cores: 2, MemoryMiB: 2048,
		RootfsStore: "x-data", DiskGiB: 30, Bridge: "vmbr0", VLAN: 20, IP: "192.168.0.51", Prefix: 24,
		Gateway: "192.168.0.1", DNS: []string{"1.1.1.1", "9.9.9.9"}, Features: []string{"nesting=1", "keyctl=1"},
		Tags: []string{"pk3s", "pk3s-k"}, OnBoot: true,
	}
	args := strings.Join(o.Args(), " ")
	for _, want := range []string{
		"create 5000 local:vztmpl/debian.tar.zst",
		"--net0 name=eth0,bridge=vmbr0,ip=192.168.0.51/24,gw=192.168.0.1,type=veth,tag=20",
		"--rootfs x-data:30", "--swap 0", "--unprivileged 0", "--tags pk3s;pk3s-k",
		"--nameserver 1.1.1.1 9.9.9.9", "--features nesting=1,keyctl=1", "--start 0",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("args missing %q:\n%s", want, args)
		}
	}
	if strings.Contains(args, "password") {
		t.Error("pct create must not set a root password")
	}
}

func TestMissingLinesIgnoresSnapshotSections(t *testing.T) {
	conf := "arch: amd64\nlxc.cap.drop:\n[snap1]\nlxc.apparmor.profile: unconfined\n"
	got := MissingLines(conf, []string{"lxc.cap.drop:", "lxc.apparmor.profile: unconfined"})
	if len(got) != 1 || got[0] != "lxc.apparmor.profile: unconfined" {
		t.Fatalf("got %v", got)
	}
}

func TestAppendConfigIsIdempotentAndRefusesSnapshots(t *testing.T) {
	f := runner.NewFake()
	f.OK("cat /etc/pve/lxc/5000.conf", "arch: amd64\nlxc.cap.drop:\n")
	f.OK("tee -a /etc/pve/lxc/5000.conf", "")
	p := PCT{R: f}
	if err := p.AppendConfig(context.Background(), 5000, []string{"lxc.cap.drop:", "lxc.mount.auto: proc:rw"}); err != nil {
		t.Fatal(err)
	}
	last := f.Calls[len(f.Calls)-1]
	if string(last.Stdin) != "lxc.mount.auto: proc:rw\n" {
		t.Fatalf("appended %q", last.Stdin)
	}

	f2 := runner.NewFake().OK("cat /etc/pve/lxc/5001.conf", "arch: amd64\n[snap]\nfoo: bar\n")
	if err := (PCT{R: f2}).AppendConfig(context.Background(), 5001, []string{"x"}); err == nil {
		t.Fatal("expected refusal for config with snapshots")
	}
}

func TestWaitReadyPollsUntilRunning(t *testing.T) {
	f := runner.NewFake()
	f.On("pct exec 5000 -- systemctl is-system-running", runner.FakeResponse{Stdout: "starting\n", ExitCode: 1})
	f.On("pct exec 5000 -- systemctl is-system-running", runner.FakeResponse{Stdout: "degraded\n", ExitCode: 1})
	slept := 0
	p := PCT{R: f, Sleep: func(time.Duration) { slept++ }}
	if err := p.WaitReady(context.Background(), 5000, time.Minute); err != nil {
		t.Fatal(err)
	}
	if slept != 1 {
		t.Errorf("expected one poll sleep, got %d", slept)
	}
}
