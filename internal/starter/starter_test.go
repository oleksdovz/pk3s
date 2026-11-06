package starter_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/oleksdovz/pk3s/internal/spec"
	"github.com/oleksdovz/pk3s/internal/starter"
	"github.com/oleksdovz/pk3s/internal/testutil"
)

func chosen() spec.ClusterSpec {
	s := spec.Default()
	s.Name = "lab"
	s.Node = "hp"
	s.CTIDStart = 5100
	s.Template = spec.Template{Storage: "DATA", Name: "debian-13-standard_13.6-1_amd64.tar.zst"}
	s.RootfsStorage = "local-lvm"
	s.Network = spec.Network{Bridge: "vmbr0", Prefix: 24, Gateway: "192.168.0.1", DNS: []string{"192.168.0.2"}, SearchDomain: "lan"}
	return s
}

func TestRenderRoundTripsAndListsHost(t *testing.T) {
	inv := testutil.HPInventory(t)
	s := chosen()
	out := starter.Render(s, inv, starter.Options{Now: time.Date(2026, 11, 6, 20, 0, 0, 0, time.UTC), StableVersion: "v1.37.2+k3s1"})
	back, err := spec.Parse([]byte(out))
	if err != nil {
		t.Fatalf("generated spec does not parse: %v\n%s", err, out)
	}
	if !reflect.DeepEqual(back, s) {
		t.Errorf("round trip differs:\n got %+v\nwant %+v", back, s)
	}
	for _, want := range []string{
		"# Host: hp, Proxmox VE 9.2.26",
		"#   local: debian-13-standard_13.1-2_amd64.tar.zst",
		"#   DATA: alpine-3.23-default_20260116_amd64.tar.xz   (not supported",
		"#   x-data                   lvmthin",
		"#   vmbr0      192.168.0.5/24, gateway 192.168.0.1",
		"#   192.168.0.6 (lxc 9000 proxmox-backup-server)",
		"# CT/VM IDs in use: 9000 proxmox-backup-server",
		"# Checked: CT IDs 5100-5102 for this spec and 5103-5112 (room to scale) are free",
		"(stable is v1.37.2+k3s1 now)",
		"# node_network:",
		"  # host_path: /data/k3s-storage",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(out, "proxmox-backup-server\n  #   NFS") || strings.Contains(out, "#   NFS ") {
		t.Error("disabled storage must not be listed")
	}
}

func TestBusyIDsAreReported(t *testing.T) {
	inv := testutil.HPInventory(t)
	s := chosen()
	s.CTIDStart = 8995 // 9000 (proxmox-backup-server) is inside 8995-9007
	out := starter.Render(s, inv, starter.Options{})
	if !strings.Contains(out, "# WARNING: CT IDs in 8995-9007 already in use: 9000 (proxmox-backup-server)") {
		t.Fatal(out[:800])
	}
}

func TestFreeCTIDStart(t *testing.T) {
	inv := testutil.HPInventory(t) // CT 9000 exists
	if got := starter.FreeCTIDStart(inv, 9000, 5); got != 9100 {
		t.Fatalf("got %d", got)
	}
	if got := starter.FreeCTIDStart(inv, 5000, 5); got != 5000 {
		t.Fatalf("got %d", got)
	}
}
