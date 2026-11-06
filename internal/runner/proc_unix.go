//go:build unix

package runner

import (
	"os/exec"
	"syscall"
)

func setProcessGroup(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessGroup(c *exec.Cmd) error {
	if c.Process == nil {
		return nil
	}
	return syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
}
