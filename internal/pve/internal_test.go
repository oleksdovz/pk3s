package pve

import "testing"

func TestStaticIPs(t *testing.T) {
	ips := staticIPs(map[string]any{
		"net0": "name=eth0,bridge=vmbr0,ip=10.0.0.5/24,gw=10.0.0.1",
		"net1": "name=eth1,bridge=vmbr1,ip=dhcp",
		"lxc":  []any{"x"},
	})
	if len(ips) != 1 || ips[0] != "10.0.0.5" {
		t.Fatalf("got %v", ips)
	}
}
