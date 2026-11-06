// Package testutil provides fixtures captured from a real Proxmox VE 9.2
// host ("hp") for tests across packages.
package testutil

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/oleksdovz/pk3s/internal/pve"
	"github.com/oleksdovz/pk3s/internal/runner"
)

func fixture(t testing.TB, name string) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	b, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "pve", "testdata", "hp", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// hpFake replays real responses captured from a Proxmox VE 9.2 host.
func HPFake(t testing.TB) *runner.Fake {
	t.Helper()
	f := runner.NewFake()
	f.OK("hostname -s", "hp\n")
	f.OK("pvesh get /nodes --output-format json", fixture(t, "nodes.json"))
	f.OK("pvesh get /version", fixture(t, "version.json"))
	f.OK("uname -m", "x86_64\n")
	f.OK("uname -r", "7.0.14-24-pve\n")
	f.OK("stat -fc %T /sys/fs/cgroup", "cgroup2fs\n")
	f.OK("pvesh get /nodes/hp/storage --output-format json", fixture(t, "storage.json"))
	f.OK("pvesh get /nodes/hp/storage/local/content", fixture(t, "content-local.json"))
	f.OK("pvesh get /nodes/hp/storage/DATA/content", fixture(t, "content-DATA.json"))
	f.OK("pvesh get /nodes/hp/network", fixture(t, "network.json"))
	f.OK("pvesh get /nodes/hp/dns", fixture(t, "dns.json"))
	f.OK("pvesh get /cluster/resources", fixture(t, "resources.json"))
	f.OK("pvesh get /nodes/hp/lxc/9000/config", fixture(t, "lxc-9000.json"))
	f.OK("cat /proc/modules", fixture(t, "proc-modules.txt"))
	f.OK("cat /lib/modules/7.0.14-24-pve/modules.builtin", fixture(t, "modules.builtin"))
	f.OK("pve-firewall status", "Status: disabled/running\n")
	f.OK("cat /etc/timezone", "Europe/Kyiv\n")
	return f
}

// HPInventory returns the inventory of the fixture host.
func HPInventory(t testing.TB) *pve.Inventory {
	t.Helper()
	inv, err := pve.Client{R: HPFake(t)}.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return inv
}
