// Package backup provides authenticated, bounded-memory backup encryption.
package backup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
)

const magic = "VpsCT-backup-v1\x00"
const chunk = 1 << 20

func Codec(key []byte) (cipher.AEAD, error) {
	b, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	return cipher.NewGCM(b)
}

// Seal authenticates an explicit end marker, preventing unnoticed truncation.
func Seal(w io.Writer, r io.Reader, a cipher.AEAD) error {
	prefix := make([]byte, 8)
	if _, e := rand.Read(prefix); e != nil {
		return e
	}
	if _, e := io.WriteString(w, magic); e != nil {
		return e
	}
	if _, e := w.Write(prefix); e != nil {
		return e
	}
	buf := make([]byte, chunk)
	for seq := uint32(0); ; seq++ {
		if seq == ^uint32(0) {
			return errors.New("backup too large")
		}
		n, e := io.ReadFull(r, buf)
		if e != nil && e != io.EOF && e != io.ErrUnexpectedEOF {
			return e
		}
		nonce := append(append([]byte{}, prefix...), make([]byte, 4)...)
		binary.BigEndian.PutUint32(nonce[8:], seq)
		sealed := a.Seal(nil, nonce, buf[:n], []byte(magic))
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(sealed)))
		if _, e = w.Write(size[:]); e != nil {
			return e
		}
		if _, e = w.Write(sealed); e != nil {
			return e
		}
		if n == 0 {
			return nil
		}
	}
}
func Open(w io.Writer, r io.Reader, a cipher.AEAD) error {
	hdr := make([]byte, len(magic)+8)
	if _, e := io.ReadFull(r, hdr); e != nil {
		return e
	}
	if string(hdr[:len(magic)]) != magic {
		return errors.New("not an encrypted VpsCT backup")
	}
	for seq := uint32(0); ; seq++ {
		if seq == ^uint32(0) {
			return errors.New("backup too large")
		}
		var size [4]byte
		if _, e := io.ReadFull(r, size[:]); e != nil {
			return e
		}
		n := binary.BigEndian.Uint32(size[:])
		if n < uint32(a.Overhead()) || n > chunk+uint32(a.Overhead()) {
			return errors.New("invalid backup record")
		}
		b := make([]byte, n)
		if _, e := io.ReadFull(r, b); e != nil {
			return e
		}
		nonce := append(append([]byte{}, hdr[len(magic):]...), size[:]...)
		binary.BigEndian.PutUint32(nonce[8:], seq)
		p, e := a.Open(nil, nonce, b, []byte(magic))
		if e != nil {
			return errors.New("backup authentication failed")
		}
		if len(p) == 0 {
			var tail [1]byte
			_, e = io.ReadFull(r, tail[:])
			if e != io.EOF {
				return errors.New("trailing backup data")
			}
			return nil
		}
		if _, e = w.Write(p); e != nil {
			return e
		}
	}
}

// File publishes the result only after the whole stream verifies and syncs.
func File(src, dst string, a cipher.AEAD, decrypt bool) error {
	in, e := os.Open(src)
	if e != nil {
		return e
	}
	defer in.Close()
	if _, e = os.Lstat(dst); !os.IsNotExist(e) {
		return errors.New("backup destination already exists or cannot be inspected")
	}
	out, e := os.CreateTemp(filepath.Dir(dst), ".backup-")
	if e != nil {
		return e
	}
	defer os.Remove(out.Name())
	defer out.Close()
	if decrypt {
		e = Open(out, in, a)
	} else {
		e = Seal(out, in, a)
	}
	if e != nil {
		return e
	}
	if e = out.Sync(); e != nil {
		return e
	}
	if e = out.Close(); e != nil {
		return e
	}
	// Link is no-replace on both supported Unix platforms.
	return os.Link(out.Name(), dst)
}
