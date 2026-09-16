package secureupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"testing"
)

func TestArchivePathsAndTypes(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind byte
		ok   bool
	}{{"ctlvpsd", tar.TypeReg, true}, {"../escape", tar.TypeReg, false}, {"/absolute", tar.TypeReg, false}, {"link", tar.TypeSymlink, false}, {"device", tar.TypeChar, false}} {
		var b bytes.Buffer
		z := gzip.NewWriter(&b)
		w := tar.NewWriter(z)
		if e := w.WriteHeader(&tar.Header{Name: tc.name, Typeflag: tc.kind, Mode: 0600}); e != nil {
			t.Fatal(e)
		}
		w.Close()
		z.Close()
		if (ValidateArchive(b.Bytes()) == nil) != tc.ok {
			t.Fatal(tc)
		}
	}
}
