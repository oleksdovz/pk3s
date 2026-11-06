package spec

import "testing"

func TestParseRejectsUnknownFields(t *testing.T) {
	if _, err := Parse([]byte("name: a\nservrs: 3\n")); err == nil {
		t.Fatal("typo in field name must be rejected")
	}
}

func TestParseKeepsDefaults(t *testing.T) {
	s, err := Parse([]byte("name: lab\nservers: 3\n"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Servers != 3 || s.Server.MemoryMiB != DefaultMemoryMiB || s.Server.DiskGiB != 30 || !s.OS.NFSClient || s.Security.Profile != ProfilePrivileged {
		t.Fatalf("%+v", s)
	}
}

func TestPrefixAndNodeKeys(t *testing.T) {
	s := Default()
	s.Name, s.Servers, s.Agents = "lab", 3, 2
	if s.Prefix() != "lab" {
		t.Fatal(s.Prefix())
	}
	s.HostnamePrefix = "k"
	if s.Prefix() != "k" || s.NodeKey(0) != "s1" || s.NodeKey(2) != "s3" || s.NodeKey(3) != "a1" {
		t.Fatal("bad keys")
	}
}

func TestSetOverride(t *testing.T) {
	var s ClusterSpec
	s.SetOverride(NodeNetwork{Node: "a1", IP: "10.0.0.5"})
	s.SetOverride(NodeNetwork{Node: "a1", IP: "10.0.0.6"})
	if o, _ := s.Override("a1"); len(s.NodeNetwork) != 1 || o.IP != "10.0.0.6" {
		t.Fatalf("%+v", s.NodeNetwork)
	}
	s.SetOverride(NodeNetwork{Node: "a1"})
	if len(s.NodeNetwork) != 0 {
		t.Fatal("empty override must be removed")
	}
}

func TestRoundTrip(t *testing.T) {
	s := Default()
	s.NodeNetwork = []NodeNetwork{{Node: "s1", IP: "10.0.0.1", DNS: []string{"1.1.1.1"}}}
	data, err := Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	back, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if back.NodeNetwork[0].IP != "10.0.0.1" {
		t.Fatal("round trip lost overrides")
	}
}
