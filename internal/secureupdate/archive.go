package secureupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"path"
	"strings"
)

// ValidateArchive bounds extraction even when an archive is correctly signed.
func ValidateArchive(data []byte) error {
	return validateArchiveReader(bytes.NewReader(data))
}
func validateArchiveReader(data io.ReadSeeker) error {
	if _, e := data.Seek(0, io.SeekStart); e != nil {
		return e
	}
	z, e := gzip.NewReader(data)
	if e != nil {
		return e
	}
	defer z.Close()
	r := tar.NewReader(io.LimitReader(z, 512<<20+1))
	var total int64
	for count := 0; ; count++ {
		h, e := r.Next()
		if e == io.EOF {
			return nil
		}
		if e != nil {
			return e
		}
		name := path.Clean(h.Name)
		if count >= 4096 || len(h.Name) > 1024 || name == ".." || strings.HasPrefix(name, "../") || strings.HasPrefix(name, "/") || strings.ContainsAny(h.Name, "\\\x00\r\n") || (h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeDir) || h.Size < 0 {
			return errors.New("invalid archive entry")
		}
		total += h.Size
		if total > 512<<20 {
			return errors.New("archive expansion exceeds limit")
		}
	}
}
