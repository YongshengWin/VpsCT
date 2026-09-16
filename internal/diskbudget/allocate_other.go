//go:build !linux

package diskbudget

import (
	"io"
	"os"
)

type zeros struct{}

func (zeros) Read(b []byte) (int, error) { clear(b); return len(b), nil }
func allocate(f *os.File, n int64) error { _, e := io.CopyN(f, zeros{}, n); return e }
