//go:build linux

package diskbudget

import (
	"golang.org/x/sys/unix"
	"os"
)

func allocate(f *os.File, n int64) error { return unix.Fallocate(int(f.Fd()), 0, 0, n) }
