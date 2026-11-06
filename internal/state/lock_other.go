//go:build !unix

package state

import (
	"errors"
	"os"
)

// On non-unix systems an exclusive-create lock file is used. A stale lock
// left by a crashed process must be removed manually (the error says where).
func lockFile(f *os.File) error {
	l, err := os.OpenFile(f.Name()+".pid", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.New("lock file " + f.Name() + ".pid exists")
	}
	return l.Close()
}

func unlockFile(f *os.File) { _ = os.Remove(f.Name() + ".pid") }
