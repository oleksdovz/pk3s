//go:build embedlinux

package remote

import "embed"

// Linux binaries embedded by `make dist` (gzip-compressed).
//
//go:embed bin/pk3s-linux-amd64.gz bin/pk3s-linux-arm64.gz
var bins embed.FS

func embedded(arch string) ([]byte, bool) {
	gz, err := bins.ReadFile("bin/pk3s-linux-" + arch + ".gz")
	if err != nil {
		return nil, false
	}
	b, err := gunzip(gz)
	return b, err == nil
}
