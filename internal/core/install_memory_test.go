package core

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestStreamingExtractionAndAtomicActivation(t *testing.T) {
	for _, format := range []string{"raw", "tar", "zip"} {
		t.Run(format, func(t *testing.T) {
			dir := t.TempDir()
			src, _ := os.CreateTemp(dir, "archive")
			defer src.Close()
			want := []byte("\x7fELFsynthetic-test-binary")
			switch format {
			case "raw":
				src.Write(want)
			case "tar":
				z := gzip.NewWriter(src)
				w := tar.NewWriter(z)
				w.WriteHeader(&tar.Header{Name: "release/sing-box", Mode: 0755, Size: int64(len(want))})
				w.Write(want)
				w.Close()
				z.Close()
			case "zip":
				w := zip.NewWriter(src)
				f, _ := w.Create("release/sing-box")
				f.Write(want)
				w.Close()
			}
			dst, _ := os.CreateTemp(dir, "stage")
			defer dst.Close()
			if err := extractBinaryTo(src, "sing-box", dst); err != nil {
				t.Fatal(err)
			}
			dst.Seek(0, io.SeekStart)
			got, _ := io.ReadAll(dst)
			if !bytes.Equal(got, want) {
				t.Fatal("wrong executable")
			}
			target := filepath.Join(dir, "sing-box")
			os.WriteFile(target, []byte("old"), 0755)
			if err := CommitBinary(dst, target); err != nil {
				t.Fatal(err)
			}
			got, _ = os.ReadFile(target)
			if !bytes.Equal(got, want) {
				t.Fatal("activation failed")
			}
		})
	}
}
func TestArchiveExpansionAndMetadataBudget(t *testing.T) {
	dir := t.TempDir()
	f, _ := os.CreateTemp(dir, "archive")
	defer f.Close()
	z := gzip.NewWriter(f)
	w := tar.NewWriter(z)
	w.WriteHeader(&tar.Header{Name: "sing-box", Mode: 0755, Size: 300 << 20})
	w.Close()
	z.Close()
	if err := extractBinaryTo(f, "sing-box", io.Discard); err == nil {
		t.Fatal("oversized expansion accepted")
	}
	f.Truncate(0)
	f.Seek(0, io.SeekStart)
	zw := zip.NewWriter(f)
	for i := 0; i < 4097; i++ {
		zw.Create("entry")
	}
	zw.Close()
	if err := extractBinaryTo(f, "sing-box", io.Discard); err == nil {
		t.Fatal("oversized directory accepted")
	}
}
