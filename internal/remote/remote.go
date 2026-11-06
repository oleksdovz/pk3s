// Package remote runs pk3s on a Proxmox VE host from a workstation
// (macOS, Windows, Linux) over the system OpenSSH client: it uploads the
// Linux pk3s binary for the host's architecture (verified by SHA-256) and
// executes it there with the user's arguments and terminal. State, logs
// and caches stay on the Proxmox host, so local and remote runs see the
// same clusters.
package remote

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/oleksdovz/pk3s/internal/runner"
)

// BinDir is where uploaded pk3s binaries live on the Proxmox host.
const BinDir = "/var/lib/pk3s/bin"

// UploadDir holds spec files uploaded for -f.
const UploadDir = "/var/lib/pk3s/uploads"

// Target describes the SSH destination.
type Target struct {
	Host     string // [user@]host
	Port     string
	Identity string // ssh -i
	// BinaryPath overrides the Linux pk3s binary to upload.
	BinaryPath string
}

// sshArgs returns the common ssh options.
func (t Target) sshArgs(tty bool) []string {
	args := []string{"-o", "BatchMode=no"}
	if tty {
		args = append(args, "-t")
	} else {
		args = append(args, "-T")
	}
	if t.Port != "" {
		args = append(args, "-p", t.Port)
	}
	if t.Identity != "" {
		args = append(args, "-i", t.Identity)
	}
	return append(args, t.Host, "--")
}

// Quote quotes one argument for a POSIX shell.
func Quote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:,+@%", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// Command joins quoted arguments into one remote shell command.
func Command(args ...string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = Quote(a)
	}
	return strings.Join(q, " ")
}

// Client runs non-interactive commands on the host.
type Client struct {
	T Target
	R runner.Runner
}

// Run executes a remote shell command, optionally feeding stdin.
func (c Client) Run(ctx context.Context, cmd string, stdin []byte) (string, error) {
	res, err := c.R.Run(ctx, runner.Cmd{Name: "ssh", Args: append(c.T.sshArgs(false), cmd), Stdin: stdin})
	out := strings.TrimSpace(string(res.Stdout))
	if err != nil {
		if res.ExitCode == 255 {
			return out, fmt.Errorf("ssh %s failed: %s", c.T.Host, strings.TrimSpace(string(res.Stderr)))
		}
		return out, err
	}
	return out, nil
}

// HostInfo is what pk3s needs to know about the remote host.
type HostInfo struct {
	Arch string
	Root bool
	PVE  bool
}

// Probe checks the host: architecture, root, Proxmox tools.
func (c Client) Probe(ctx context.Context) (HostInfo, error) {
	out, err := c.Run(ctx, `uname -m; id -u; command -v pct >/dev/null && echo pve || echo nopve`, nil)
	if err != nil {
		return HostInfo{}, err
	}
	f := strings.Fields(out)
	if len(f) < 3 {
		return HostInfo{}, fmt.Errorf("unexpected answer from %s: %q", c.T.Host, out)
	}
	arch := f[0]
	switch arch {
	case "x86_64":
		arch = "amd64"
	case "aarch64":
		arch = "arm64"
	}
	return HostInfo{Arch: arch, Root: f[1] == "0", PVE: f[2] == "pve"}, nil
}

// Binary returns the Linux pk3s binary for arch: the -remote-bin override,
// the copy embedded in this build, this executable (Linux, same arch), or
// pk3s-linux-<arch> next to this executable or in ./dist.
func Binary(arch, override string) ([]byte, string, error) {
	if override != "" {
		b, err := os.ReadFile(override)
		return b, override, err
	}
	if b, ok := embedded(arch); ok {
		return b, "embedded", nil
	}
	if runtime.GOOS == "linux" && runtime.GOARCH == arch {
		if exe, err := os.Executable(); err == nil {
			b, err := os.ReadFile(exe)
			return b, exe, err
		}
	}
	name := "pk3s-linux-" + arch
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), name))
	}
	candidates = append(candidates, filepath.Join("dist", name))
	for _, p := range candidates {
		if b, err := os.ReadFile(p); err == nil {
			return b, p, nil
		}
	}
	return nil, "", fmt.Errorf("no Linux %s pk3s binary to upload: build with `make dist` (embeds it) or pass --remote-bin PATH", arch)
}

func gunzip(b []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// Install uploads bin to the host unless an identical copy is there, and
// returns its remote path.
func (c Client) Install(ctx context.Context, bin []byte) (string, error) {
	sum := sha256.Sum256(bin)
	hexsum := hex.EncodeToString(sum[:])
	path := BinDir + "/pk3s-" + hexsum[:16]
	check := Command("sha256sum", path) + " 2>/dev/null | cut -d' ' -f1"
	if out, err := c.Run(ctx, check, nil); err == nil && out == hexsum {
		return path, nil
	}
	tmp := path + ".upload"
	upload := fmt.Sprintf("set -e; mkdir -p -m 0700 %s; umask 077; cat > %s; chmod 0755 %s",
		Quote(BinDir), Quote(tmp), Quote(tmp))
	if _, err := c.Run(ctx, upload, bin); err != nil {
		return "", fmt.Errorf("upload pk3s to %s: %w", c.T.Host, err)
	}
	out, err := c.Run(ctx, Command("sha256sum", tmp)+" | cut -d' ' -f1", nil)
	if err != nil {
		return "", err
	}
	if out != hexsum {
		_, _ = c.Run(ctx, Command("rm", "-f", tmp), nil)
		return "", fmt.Errorf("uploaded binary checksum mismatch on %s", c.T.Host)
	}
	if _, err := c.Run(ctx, Command("mv", "-f", tmp, path), nil); err != nil {
		return "", err
	}
	return path, nil
}

// Upload copies a local file to the host and returns the remote path.
func (c Client) Upload(ctx context.Context, local string) (string, error) {
	data, err := os.ReadFile(local)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	remote := fmt.Sprintf("%s/%s-%s", UploadDir, hex.EncodeToString(sum[:6]), filepath.Base(local))
	cmd := fmt.Sprintf("set -e; mkdir -p -m 0700 %s; umask 077; cat > %s", Quote(UploadDir), Quote(remote))
	if _, err := c.Run(ctx, cmd, data); err != nil {
		return "", fmt.Errorf("upload %s: %w", local, err)
	}
	return remote, nil
}

// Plan is how the local arguments map onto the remote run.
type Plan struct {
	Args []string // arguments for the remote pk3s
	// SaveTo: write the remote stdout to this local file (init -o FILE).
	SaveTo string
	// Uploads: local file -> index in Args to replace with the remote path.
	Uploads map[int]string
}

// Rewrite adapts arguments that refer to local files.
func Rewrite(args []string) Plan {
	p := Plan{Uploads: map[int]string{}}
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-f" && i+1 < len(args):
			p.Args = append(p.Args, a, args[i+1])
			p.Uploads[len(p.Args)-1] = args[i+1]
			i++
		case strings.HasPrefix(a, "-f="):
			p.Args = append(p.Args, "-f", strings.TrimPrefix(a, "-f="))
			p.Uploads[len(p.Args)-1] = strings.TrimPrefix(a, "-f=")
		case cmd == "init" && a == "-o" && i+1 < len(args):
			if args[i+1] != "-" {
				p.SaveTo = args[i+1]
			}
			p.Args = append(p.Args, "-o", "-")
			i++
		case cmd == "init" && strings.HasPrefix(a, "-o="):
			if v := strings.TrimPrefix(a, "-o="); v != "-" {
				p.SaveTo = v
			}
			p.Args = append(p.Args, "-o", "-")
		default:
			p.Args = append(p.Args, a)
		}
	}
	if cmd == "init" && p.SaveTo == "" && !contains(p.Args, "-o") {
		// Without -o the remote side would write NAME.yaml on the host;
		// write it locally instead.
		if len(p.Args) > 1 && !strings.HasPrefix(p.Args[1], "-") {
			p.SaveTo = p.Args[1] + ".yaml"
			p.Args = append(p.Args, "-o", "-")
		}
	}
	return p
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Exec runs pk3s on the host with the user's terminal attached. It returns
// the remote exit code.
func Exec(ctx context.Context, t Target, remoteBin string, args []string, stdout io.Writer, tty bool) (int, error) {
	full := append(t.sshArgs(tty), Command(append([]string{remoteBin}, args...)...))
	cmd := exec.CommandContext(ctx, "ssh", full...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, stdout, os.Stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	if err != nil {
		return 1, fmt.Errorf("run ssh: %w (is the OpenSSH client installed?)", err)
	}
	return 0, nil
}
