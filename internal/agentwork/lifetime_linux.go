//go:build linux

package agentwork

import (
	"os/exec"
	"syscall"
)

func bindLifetime(c *exec.Cmd) { c.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL} }
