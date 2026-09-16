package maintenance

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const logSegment = 4 << 20

// boundedLog drains child output continuously while retaining at most two
// segments. The worker result is persisted independently of these diagnostics.
type boundedLog struct {
	mu   sync.Mutex
	file *os.File
	path string
	size int64
}

func openLog(path string) (*boundedLog, error) {
	if st, e := os.Lstat(path + ".1"); e == nil {
		if !st.Mode().IsRegular() {
			return nil, errors.New("invalid log rotation target")
		}
		old, err := openLogFile(path + ".1")
		if err != nil {
			return nil, err
		}
		err = trimLog(old)
		old.Close()
		if err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	f, e := openLogFile(path)
	if e != nil {
		return nil, e
	}
	if e = trimLog(f); e != nil {
		f.Close()
		return nil, e
	}
	st, e := f.Stat()
	if e != nil {
		f.Close()
		return nil, e
	}
	l := &boundedLog{file: f, path: path, size: st.Size()}
	if _, e = f.Seek(0, io.SeekEnd); e != nil {
		f.Close()
		return nil, e
	}
	return l, nil
}
func trimLog(f *os.File) error {
	st, e := f.Stat()
	if e != nil {
		return e
	}
	if st.Size() <= logSegment {
		return nil
	}
	tail := make([]byte, logSegment)
	if _, e = f.ReadAt(tail, st.Size()-logSegment); e != nil {
		return e
	}
	if _, e = f.WriteAt(tail, 0); e != nil {
		return e
	}
	return f.Truncate(logSegment)
}
func openLogFile(path string) (*os.File, error) {
	fd, e := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), path)
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("invalid worker log file")
	}
	return f, nil
}
func (l *boundedLog) rotate() error {
	if e := l.file.Close(); e != nil {
		return e
	}
	old := l.path + ".1"
	if st, e := os.Lstat(old); e == nil && !st.Mode().IsRegular() {
		return errors.New("invalid log rotation target")
	}
	if e := os.Rename(l.path, old); e != nil {
		return e
	}
	f, e := openLogFile(l.path)
	if e != nil {
		return e
	}
	l.file = f
	l.size = 0
	return nil
}
func (l *boundedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(p)
	written := 0
	for len(p) > 0 {
		if l.size >= logSegment {
			if e := l.rotate(); e != nil {
				return written, e
			}
		}
		amount := min(len(p), logSegment-int(l.size))
		k, e := l.file.Write(p[:amount])
		written += k
		l.size += int64(k)
		if e != nil {
			return written, e
		}
		if k == 0 {
			return written, io.ErrShortWrite
		}
		p = p[k:]
	}
	return n, nil
}
func (l *boundedLog) Close() error { l.mu.Lock(); defer l.mu.Unlock(); return l.file.Close() }

// pruneDiagnostics never removes status records (idempotency), active tasks,
// previous binaries or data snapshots. It only removes old terminal logs.
func (m *Manager) pruneDiagnostics() error {
	jobs := m.List()
	var retained int64
	for i, j := range jobs {
		for _, name := range []string{"worker.log", "worker.log.1"} {
			p := m.path(j.ID, name)
			st, e := os.Lstat(p)
			if os.IsNotExist(e) {
				continue
			}
			if e != nil {
				return e
			}
			if !st.Mode().IsRegular() {
				return errors.New("invalid diagnostic file")
			}
			expired := i >= 100 || time.Since(j.UpdatedAt) > 90*24*time.Hour
			// Leave room for the next active worker's two bounded segments.
			if !j.Active() && (expired || retained+st.Size() > (128<<20)-2*logSegment) {
				if e = os.Remove(p); e != nil {
					return e
				}
			} else {
				retained += st.Size()
			}
		}
	}
	if retained > 128<<20 {
		return errors.New("活动维护日志超过预算")
	}
	entries, e := os.ReadDir(filepath.Clean(m.Dir))
	if e != nil {
		return e
	}
	if len(entries) > 10000 {
		return errors.New("维护记录数量达到上限，请先归档历史状态")
	}
	return nil
}
