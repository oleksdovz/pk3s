package state

import (
	"os"
	"testing"
)

func TestStoreFilesArePrivate(t *testing.T) {
	s := &Store{Root: t.TempDir(), Cluster: "lab"}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	st := New("lab", "v1.37.2+k3s1")
	st.AddResource(Resource{Kind: KindContainer, ID: "5000"})
	st.AddResource(Resource{Kind: KindContainer, ID: "5000"})
	if err := s.Save(st); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSecrets(&Secrets{ServerToken: "x"}); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"state.json", "secrets.json"} {
		fi, err := os.Stat(s.Path(f))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v err %v", f, fi.Mode(), err)
		}
	}
	if fi, _ := os.Stat(s.Dir()); fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", fi.Mode())
	}
	back, err := s.Load()
	if err != nil || len(back.Resources) != 1 || !back.HasResource(KindContainer, "5000") {
		t.Fatalf("ledger: %+v %v", back, err)
	}
	names, _ := List(s.Root)
	if len(names) != 1 || names[0] != "lab" {
		t.Fatalf("list: %v", names)
	}
}

func TestLockIsExclusive(t *testing.T) {
	root := t.TempDir()
	a := &Store{Root: root, Cluster: "lab"}
	b := &Store{Root: root, Cluster: "lab"}
	if err := a.Init(); err != nil {
		t.Fatal(err)
	}
	if err := a.Lock(); err != nil {
		t.Fatal(err)
	}
	if err := b.Lock(); err == nil {
		t.Fatal("second lock must fail")
	}
	a.Unlock()
	if err := b.Lock(); err != nil {
		t.Fatalf("lock after unlock: %v", err)
	}
	b.Unlock()
}
