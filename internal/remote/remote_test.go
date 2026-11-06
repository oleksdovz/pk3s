package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/oleksdovz/pk3s/internal/runner"
)

func TestQuote(t *testing.T) {
	cases := map[string]string{
		"plan":                 "plan",
		"/var/lib/pk3s/x.yaml": "/var/lib/pk3s/x.yaml",
		"":                     "''",
		"a b":                  "'a b'",
		"it's":                 `'it'"'"'s'`,
		"$(reboot)":            "'$(reboot)'",
		"a;b":                  "'a;b'",
	}
	for in, want := range cases {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestRewrite(t *testing.T) {
	p := Rewrite([]string{"apply", "-f", "cluster.yaml"})
	if !reflect.DeepEqual(p.Args, []string{"apply", "-f", "cluster.yaml"}) || p.Uploads[2] != "cluster.yaml" {
		t.Fatalf("%+v", p)
	}
	p = Rewrite([]string{"init", "lab", "-o", "lab.yaml"})
	if p.SaveTo != "lab.yaml" || !reflect.DeepEqual(p.Args, []string{"init", "lab", "-o", "-"}) {
		t.Fatalf("%+v", p)
	}
	p = Rewrite([]string{"init", "lab"})
	if p.SaveTo != "lab.yaml" || !reflect.DeepEqual(p.Args, []string{"init", "lab", "-o", "-"}) {
		t.Fatalf("init without -o must be written locally: %+v", p)
	}
	p = Rewrite([]string{"init", "lab", "-o", "-"})
	if p.SaveTo != "" {
		t.Fatalf("-o - prints to the local stdout: %+v", p)
	}
	p = Rewrite([]string{"status", "lab"})
	if p.SaveTo != "" || len(p.Uploads) != 0 {
		t.Fatalf("%+v", p)
	}
}

func TestProbe(t *testing.T) {
	f := runner.NewFake().OK("ssh -o BatchMode=no -T root@hp --", "x86_64\n0\npve\n")
	info, err := Client{T: Target{Host: "root@hp"}, R: f}.Probe(context.Background())
	if err != nil || info.Arch != "amd64" || !info.Root || !info.PVE {
		t.Fatalf("%+v %v", info, err)
	}
}

func TestInstallSkipsIdenticalBinary(t *testing.T) {
	bin := []byte("linux binary")
	sum := sha256.Sum256(bin)
	hexsum := hex.EncodeToString(sum[:])
	path := BinDir + "/pk3s-" + hexsum[:16]

	f := runner.NewFake().OK("ssh -o BatchMode=no -T root@hp -- sha256sum "+path, hexsum+"\n")
	got, err := Client{T: Target{Host: "root@hp"}, R: f}.Install(context.Background(), bin)
	if err != nil || got != path || len(f.Calls) != 1 {
		t.Fatalf("expected reuse without upload: %s %v %v", got, err, f.CallLines())
	}

	f = runner.NewFake()
	f.On("ssh -o BatchMode=no -T root@hp -- sha256sum "+path+" ", runner.FakeResponse{Stdout: ""})
	f.On("ssh -o BatchMode=no -T root@hp -- set -e", runner.FakeResponse{})
	f.On("ssh -o BatchMode=no -T root@hp -- sha256sum "+path+".upload", runner.FakeResponse{Stdout: hexsum})
	f.On("ssh -o BatchMode=no -T root@hp -- mv -f", runner.FakeResponse{})
	if _, err := (Client{T: Target{Host: "root@hp"}, R: f}).Install(context.Background(), bin); err != nil {
		t.Fatalf("%v %v", err, f.CallLines())
	}
	uploaded := false
	for _, c := range f.Calls {
		if strings.Contains(strings.Join(c.Args, " "), "cat > ") && string(c.Stdin) == string(bin) {
			uploaded = true
		}
	}
	if !uploaded {
		t.Fatalf("binary not uploaded via stdin: %v", f.CallLines())
	}
}

func TestInstallRejectsCorruptUpload(t *testing.T) {
	f := runner.NewFake()
	f.Strict = false
	f.On("ssh -o BatchMode=no -T root@hp -- sha256sum", runner.FakeResponse{Stdout: "deadbeef"})
	_, err := Client{T: Target{Host: "root@hp"}, R: f}.Install(context.Background(), []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
}

func TestBinaryOverride(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pk3s")
	if err := os.WriteFile(p, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	b, src, err := Binary("amd64", p)
	if err != nil || string(b) != "bin" || src != p {
		t.Fatalf("%q %s %v", b, src, err)
	}
}

func TestSSHArgs(t *testing.T) {
	got := Target{Host: "root@hp", Port: "2222", Identity: "/k"}.sshArgs(true)
	want := []string{"-o", "BatchMode=no", "-t", "-p", "2222", "-i", "/k", "root@hp", "--"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
}
