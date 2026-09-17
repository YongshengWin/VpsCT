package safehttp

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestCopyBounded(t *testing.T) {
	for _, size := range []int{0, 3, 4, 5} {
		var dst bytes.Buffer
		n, err := CopyBounded(&dst, strings.NewReader(strings.Repeat("x", size)), 4)
		if n != int64(min(size, 4)) || dst.Len() > 4 || (size > 4) != errors.Is(err, ErrSize) {
			t.Fatalf("size=%d n=%d err=%v", size, n, err)
		}
	}
}

type repeatingReader struct{}

func (repeatingReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }
func BenchmarkStreamArtifact128MiB(b *testing.B) {
	b.ReportAllocs()
	b.SetBytes(128 << 20)
	for i := 0; i < b.N; i++ {
		if _, err := CopyBounded(io.Discard, io.LimitReader(repeatingReader{}, 128<<20), 128<<20); err != nil {
			b.Fatal(err)
		}
	}
}
