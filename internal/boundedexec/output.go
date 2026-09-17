// Package boundedexec bounds output retained from short-lived host commands.
package boundedexec

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

var ErrOutputLimit = errors.New("command output exceeds memory budget")

type capture struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (w *capture) Write(p []byte) (int, error) {
	n := len(p)
	room := w.limit - w.buf.Len()
	if n > room {
		w.overflow = true
		p = p[:max(0, room)]
	}
	_, _ = w.buf.Write(p)
	return n, nil // drain after overflow so the child cannot block on its pipe
}

// Run never treats a truncated result as valid. Timeout also bounds children
// which continue producing output after exhausting the capture budget.
func Run(ctx context.Context, input string, limit int, name string, args ...string) ([]byte, string, error) {
	if limit < 0 || limit > 8<<20 {
		return nil, "", ErrOutputLimit
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = time.Second
	if input != "" {
		cmd.Stdin = bytes.NewBufferString(input)
	}
	out, stderr := capture{limit: limit}, capture{limit: 16 << 10}
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if out.overflow || stderr.overflow {
		err = ErrOutputLimit
	}
	return out.buf.Bytes(), stderr.buf.String(), err
}
