// Package diskbudget rejects growing maintenance work before service shutdown.
package diskbudget

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func checkSpace(capacity, available, inodes uint64, bytes int64, files uint64) error {
	if bytes < 0 || bytes > 64<<30 || files > 1<<20 {
		return errors.New("invalid maintenance space budget")
	}
	reserve := max(uint64(256<<20), capacity/20)
	if available < reserve || uint64(bytes) > available-reserve {
		return fmt.Errorf("维护空间不足：需额外 %d MiB，且保留至少 %d MiB", (bytes+(1<<20)-1)>>20, reserve>>20)
	}
	if inodes < files+128 {
		return errors.New("维护 inode 余量不足")
	}
	return nil
}
func Check(dir string, bytes int64, files uint64) error {
	var st unix.Statfs_t
	if e := unix.Statfs(dir, &st); e != nil {
		return e
	}
	free := uint64(st.Ffree)
	if st.Files == 0 {
		// Some overlay/dynamic-inode filesystems expose no fixed inode pool.
		// Probe actual allocation instead of interpreting "unreported" as full.
		probe, err := os.MkdirTemp(dir, ".ctlvps-inode-probe-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(probe)
		for i := 0; i < 16; i++ {
			f, err := os.CreateTemp(probe, "inode-")
			if err != nil {
				return err
			}
			if err = f.Close(); err != nil {
				return err
			}
		}
		free = ^uint64(0)
	}
	return checkSpace(uint64(st.Blocks)*uint64(st.Bsize), uint64(st.Bavail)*uint64(st.Bsize), free, bytes, files)
}

// Reserve allocates real blocks on the target filesystem. Callers keep their
// installation lock and release just before consuming this budget; this does
// not claim to reserve space against unrelated privileged system processes.
func Reserve(dir string, bytes int64) (*os.File, error) {
	if e := Check(dir, bytes, 16); e != nil {
		return nil, e
	}
	f, e := os.CreateTemp(filepath.Clean(dir), ".ctlvps-reserve-")
	if e != nil {
		return nil, e
	}
	if e = allocate(f, bytes); e != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, e
	}
	return f, nil
}
func Release(f *os.File) error {
	if f == nil {
		return nil
	}
	name := f.Name()
	e := f.Close()
	r := os.Remove(name)
	return errors.Join(e, r)
}
