//go:build !embedlinux

package remote

func embedded(string) ([]byte, bool) { return nil, false }
