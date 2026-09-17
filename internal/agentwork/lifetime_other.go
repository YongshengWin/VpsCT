//go:build !linux

package agentwork

import "os/exec"

func bindLifetime(c *exec.Cmd) {}
