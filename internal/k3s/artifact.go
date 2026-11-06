// Package k3s handles k3s release artifacts (download + checksum
// verification), configuration rendering and readiness parsing.
package k3s

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var versionRe = regexp.MustCompile(`^v\d+\.\d+\.\d+\+k3s\d+$`)
var channelRe = regexp.MustCompile(`^(stable|latest|v\d+\.\d+)$`)

// IsExactVersion reports whether v is a concrete release tag.
func IsExactVersion(v string) bool { return versionRe.MatchString(v) }

// IsChannel reports whether v is a release channel name.
func IsChannel(v string) bool { return channelRe.MatchString(v) }

// ChannelsURL is the official k3s release channel server.
const ChannelsURL = "https://update.k3s.io/v1-release/channels"

// ReleaseBaseURL is where release assets are downloaded from.
const ReleaseBaseURL = "https://github.com/k3s-io/k3s/releases/download"

// BinaryName returns the release asset name for an architecture.
func BinaryName(arch string) (string, error) {
	switch arch {
	case "amd64":
		return "k3s", nil
	case "arm64":
		return "k3s-arm64", nil
	default:
		return "", fmt.Errorf("unsupported architecture %q (supported: amd64, arm64)", arch)
	}
}

// ChecksumName returns the checksum file asset name for an architecture.
func ChecksumName(arch string) string { return "sha256sum-" + arch + ".txt" }

// AssetURL builds the download URL for a release asset.
func AssetURL(base, version, asset string) string {
	return fmt.Sprintf("%s/%s/%s", strings.TrimRight(base, "/"), strings.ReplaceAll(url.PathEscape(version), "+", "%2B"), asset)
}

// Fetcher downloads artifacts over HTTPS.
type Fetcher struct {
	Client      *http.Client
	ChannelsURL string
	ReleaseBase string
}

// NewFetcher returns a Fetcher with sane timeouts.
func NewFetcher() Fetcher {
	return Fetcher{
		Client:      &http.Client{Timeout: 10 * time.Minute},
		ChannelsURL: ChannelsURL,
		ReleaseBase: ReleaseBaseURL,
	}
}

func (f Fetcher) get(ctx context.Context, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "pk3s")
	resp, err := f.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	return resp, nil
}

// ResolveChannel returns the latest release in a channel.
func (f Fetcher) ResolveChannel(ctx context.Context, channel string) (string, error) {
	resp, err := f.get(ctx, f.ChannelsURL)
	if err != nil {
		return "", fmt.Errorf("resolve k3s channel %q: %w", channel, err)
	}
	defer resp.Body.Close()
	return parseChannels(resp.Body, channel)
}

func parseChannels(r io.Reader, channel string) (string, error) {
	var doc struct {
		Data []struct {
			Name   string `json:"name"`
			Latest string `json:"latest"`
		} `json:"data"`
		Channels []struct {
			Name   string `json:"name"`
			Latest string `json:"latest"`
		} `json:"channels"`
	}
	if err := json.NewDecoder(r).Decode(&doc); err != nil {
		return "", fmt.Errorf("decode channels: %w", err)
	}
	all := append(doc.Data, doc.Channels...)
	for _, c := range all {
		if c.Name == channel {
			if !IsExactVersion(c.Latest) {
				return "", fmt.Errorf("channel %q has no usable release (%q)", channel, c.Latest)
			}
			return c.Latest, nil
		}
	}
	return "", fmt.Errorf("unknown k3s channel %q", channel)
}

// ParseChecksums parses a sha256sum file into name -> hex digest.
func ParseChecksums(r io.Reader) (map[string]string, error) {
	sums := map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 {
			continue
		}
		sum := strings.ToLower(f[0])
		if len(sum) != 64 {
			continue
		}
		if _, err := hex.DecodeString(sum); err != nil {
			continue
		}
		sums[strings.TrimPrefix(f[1], "*")] = sum
	}
	return sums, sc.Err()
}

// FileSHA256 returns the hex SHA-256 of a file.
func FileSHA256(path string) (string, error) {
	fh, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer fh.Close()
	h := sha256.New()
	if _, err := io.Copy(h, fh); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Artifact is a verified k3s binary on the host.
type Artifact struct {
	Version string
	Arch    string
	Path    string
	SHA256  string
}

// EnsureBinary downloads (or reuses from cacheDir) the k3s binary for
// version/arch and verifies it against the release checksum file. A cached
// binary is re-verified on every call.
func (f Fetcher) EnsureBinary(ctx context.Context, cacheDir, version, arch string) (Artifact, error) {
	if !IsExactVersion(version) {
		return Artifact{}, fmt.Errorf("refusing to download unpinned version %q", version)
	}
	bin, err := BinaryName(arch)
	if err != nil {
		return Artifact{}, err
	}
	dir := filepath.Join(cacheDir, "k3s", version)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Artifact{}, err
	}

	sumPath := filepath.Join(dir, ChecksumName(arch))
	if _, err := os.Stat(sumPath); err != nil {
		if err := f.download(ctx, AssetURL(f.ReleaseBase, version, ChecksumName(arch)), sumPath); err != nil {
			return Artifact{}, err
		}
	}
	sf, err := os.Open(sumPath)
	if err != nil {
		return Artifact{}, err
	}
	sums, err := ParseChecksums(sf)
	sf.Close()
	if err != nil {
		return Artifact{}, err
	}
	want, ok := sums[bin]
	if !ok {
		return Artifact{}, fmt.Errorf("checksum for %s not found in %s", bin, ChecksumName(arch))
	}

	binPath := filepath.Join(dir, bin)
	if got, err := FileSHA256(binPath); err == nil && got == want {
		return Artifact{Version: version, Arch: arch, Path: binPath, SHA256: want}, nil
	}
	tmp := binPath + ".partial"
	if err := f.download(ctx, AssetURL(f.ReleaseBase, version, bin), tmp); err != nil {
		return Artifact{}, err
	}
	got, err := FileSHA256(tmp)
	if err != nil {
		return Artifact{}, err
	}
	if got != want {
		os.Remove(tmp)
		return Artifact{}, fmt.Errorf("checksum mismatch for %s %s: expected %s, got %s", bin, version, want, got)
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return Artifact{}, err
	}
	if err := os.Rename(tmp, binPath); err != nil {
		return Artifact{}, err
	}
	return Artifact{Version: version, Arch: arch, Path: binPath, SHA256: want}, nil
}

func (f Fetcher) download(ctx context.Context, u, dst string) error {
	resp, err := f.get(ctx, u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		os.Remove(dst)
		return fmt.Errorf("download %s: %w", u, err)
	}
	return out.Close()
}

// Version is a parsed k3s release tag (v1.37.2+k3s1).
type Version struct{ Major, Minor, Patch, Rev int }

// ParseVersion parses an exact k3s release tag.
func ParseVersion(v string) (Version, bool) {
	var out Version
	if !IsExactVersion(v) {
		return out, false
	}
	_, err := fmt.Sscanf(v, "v%d.%d.%d+k3s%d", &out.Major, &out.Minor, &out.Patch, &out.Rev)
	return out, err == nil
}

// Compare returns -1, 0 or 1.
func (a Version) Compare(b Version) int {
	for _, d := range [][2]int{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}, {a.Rev, b.Rev}} {
		if d[0] < d[1] {
			return -1
		}
		if d[0] > d[1] {
			return 1
		}
	}
	return 0
}
