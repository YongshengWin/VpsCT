//go:build !linux

package agentnet

import "os/exec"

func isolate(c *exec.Cmd) error { return nil }
