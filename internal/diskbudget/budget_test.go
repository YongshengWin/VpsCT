package diskbudget

import (
	"os"
	"testing"
)

func TestMaintenanceHeadroom(t *testing.T) {
	for _, c := range []struct {
		capacity, available, inodes uint64
		bytes                       int64
		ok                          bool
	}{
		{10 << 30, 2 << 30, 2000, 512 << 20, true}, {10 << 30, 600 << 20, 2000, 512 << 20, false}, {1 << 30, 300 << 20, 2000, 100 << 20, false}, {10 << 30, 2 << 30, 100, 512 << 20, false},
	} {
		e := checkSpace(c.capacity, c.available, c.inodes, c.bytes, 16)
		if (e == nil) != c.ok {
			t.Fatalf("budget result %v for %+v", e, c)
		}
	}
}
func TestReservationAllocatesAndReleases(t *testing.T) {
	f, e := Reserve(t.TempDir(), 1<<20)
	if e != nil {
		t.Fatal(e)
	}
	st, e := f.Stat()
	if e != nil || st.Size() != 1<<20 {
		t.Fatal(st, e)
	}
	p := f.Name()
	if e = Release(f); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(p); !os.IsNotExist(e) {
		t.Fatal("reservation leaked")
	}
}
