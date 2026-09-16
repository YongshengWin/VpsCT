package backup

import (
	"bytes"
	"testing"
)

func TestAuthenticatedRoundTrip(t *testing.T) {
	a, _ := Codec(make([]byte, 32))
	src := bytes.Repeat([]byte("backup-fixture"), 200000)
	var sealed bytes.Buffer
	if e := Seal(&sealed, bytes.NewReader(src), a); e != nil {
		t.Fatal(e)
	}
	var out bytes.Buffer
	if e := Open(&out, bytes.NewReader(sealed.Bytes()), a); e != nil || !bytes.Equal(out.Bytes(), src) {
		t.Fatal(e)
	}
	for _, bad := range [][]byte{sealed.Bytes()[:sealed.Len()-1], append(append([]byte{}, sealed.Bytes()...), 0)} {
		if Open(&bytes.Buffer{}, bytes.NewReader(bad), a) == nil {
			t.Fatal("truncation/trailing accepted")
		}
	}
	tampered := append([]byte{}, sealed.Bytes()...)
	tampered[len(tampered)/2] ^= 1
	if Open(&bytes.Buffer{}, bytes.NewReader(tampered), a) == nil {
		t.Fatal("tampering accepted")
	}
}
