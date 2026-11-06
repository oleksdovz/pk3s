//go:build !unix

package runner

import "os/exec"

func setProcessGroup(*exec.Cmd) {}

func killProcessGroup(c *exec.Cmd) error {
	if c.Process == nil {
		return nil
	}
	return c.Process.Kill()
}
