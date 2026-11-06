package k3s

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestVersionAndChannel(t *testing.T) {
	for v, exact := range map[string]bool{"v1.37.2+k3s1": true, "v1.37.2": false, "stable": false, "1.37.2+k3s1": false} {
		if IsExactVersion(v) != exact {
			t.Errorf("IsExactVersion(%q) != %v", v, exact)
		}
	}
	for _, c := range []string{"stable", "latest", "v1.36"} {
		if !IsChannel(c) {
			t.Errorf("%q should be a channel", c)
		}
	}
	if IsChannel("v1.36.6+k3s1") || IsChannel("testing") {
		t.Error("exact versions and the testing channel are not accepted as channels")
	}
}

func TestParseChannelsRealShape(t *testing.T) {
	doc := `{"type":"collection","data":[{"id":"stable","name":"stable","latest":"v1.37.2+k3s1"},{"name":"v1.36","latest":"v1.36.6+k3s1"},{"name":"v1.16-testing","latest":""}]}`
	v, err := parseChannels(strings.NewReader(doc), "v1.36")
	if err != nil || v != "v1.36.6+k3s1" {
		t.Fatalf("got %q %v", v, err)
	}
	if _, err := parseChannels(strings.NewReader(doc), "v1.16-testing"); err == nil {
		t.Error("expected error for channel without release")
	}
}

func TestAssetURLEscapesPlus(t *testing.T) {
	got := AssetURL(ReleaseBaseURL, "v1.37.2+k3s1", "k3s")
	if got != "https://github.com/k3s-io/k3s/releases/download/v1.37.2%2Bk3s1/k3s" {
		t.Fatal(got)
	}
}

func releaseServer(t *testing.T, binary []byte, sum string) (*httptest.Server, *int) {
	t.Helper()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.37.2+k3s1/sha256sum-amd64.txt":
			fmt.Fprintf(w, "%s  k3s\n%s  k3s-airgap-images-amd64.tar\n", sum, strings.Repeat("0", 64))
		case "/v1.37.2+k3s1/k3s":
			hits++
			w.Write(binary)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestEnsureBinaryVerifiesAndCaches(t *testing.T) {
	bin := []byte("#!fake k3s binary")
	h := sha256.Sum256(bin)
	srv, hits := releaseServer(t, bin, hex.EncodeToString(h[:]))
	f := Fetcher{Client: srv.Client(), ReleaseBase: srv.URL}
	dir := t.TempDir()

	a, err := f.EnsureBinary(context.Background(), dir, "v1.37.2+k3s1", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(a.Path); string(got) != string(bin) {
		t.Fatal("binary content mismatch")
	}
	if _, err := f.EnsureBinary(context.Background(), dir, "v1.37.2+k3s1", "amd64"); err != nil {
		t.Fatal(err)
	}
	if *hits != 1 {
		t.Errorf("binary downloaded %d times, want 1 (cache)", *hits)
	}
}

func TestEnsureBinaryRejectsChecksumMismatch(t *testing.T) {
	srv, _ := releaseServer(t, []byte("tampered"), strings.Repeat("a", 64))
	f := Fetcher{Client: srv.Client(), ReleaseBase: srv.URL}
	dir := t.TempDir()
	_, err := f.EnsureBinary(context.Background(), dir, "v1.37.2+k3s1", "amd64")
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
	if _, err := os.Stat(dir + "/k3s/v1.37.2+k3s1/k3s"); err == nil {
		t.Error("unverified binary must not be left in the cache")
	}
}

func TestEnsureBinaryRefusesChannel(t *testing.T) {
	if _, err := (Fetcher{}).EnsureBinary(context.Background(), t.TempDir(), "stable", "amd64"); err == nil {
		t.Fatal("expected refusal for unpinned version")
	}
}

func TestNodeConfigRender(t *testing.T) {
	out, err := NodeConfig{Token: "secret-token", ClusterInit: true, Disable: []string{"traefik"}, WriteKubeconfigMode: "0600"}.Render()
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"token: secret-token", "cluster-init: true", "- traefik", `write-kubeconfig-mode: "0600"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	if strings.Contains(s, "server:") {
		t.Error("empty fields must be omitted")
	}
}

func TestParseNodes(t *testing.T) {
	doc := `{"items":[
	 {"metadata":{"name":"k-s1","labels":{"node-role.kubernetes.io/control-plane":"true"}},
	  "status":{"conditions":[{"type":"Ready","status":"True"}],"addresses":[{"type":"InternalIP","address":"10.0.0.1"}],"nodeInfo":{"kubeletVersion":"v1.37.2+k3s1"}}},
	 {"metadata":{"name":"k-a1","labels":{}},"status":{"conditions":[{"type":"Ready","status":"False"}]}}]}`
	nodes, err := ParseNodes([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 || !nodes[0].Ready || nodes[1].Ready || nodes[0].IP != "10.0.0.1" || nodes[0].Roles[0] != "control-plane" {
		t.Fatalf("%+v", nodes)
	}
}

func TestRewriteKubeconfig(t *testing.T) {
	in := "clusters:\n- cluster:\n    server: https://127.0.0.1:6443\n  name: default\ncurrent-context: default\n"
	out := string(RewriteKubeconfig([]byte(in), "192.168.0.51", "lab"))
	if !strings.Contains(out, "https://192.168.0.51:6443") || !strings.Contains(out, "current-context: lab") {
		t.Fatal(out)
	}
}
