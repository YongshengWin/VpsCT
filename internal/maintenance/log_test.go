package maintenance

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkerOutputBoundedAndKeepsTail(t *testing.T) {
	p := filepath.Join(t.TempDir(), "worker.log")
	l, e := openLog(p)
	if e != nil {
		t.Fatal(e)
	}
	raw := bytes.Repeat([]byte("x"), logSegment*5)
	if n, e := l.Write(raw); e != nil || n != len(raw) {
		t.Fatal(n, e)
	}
	l.Write([]byte("last error"))
	l.Close()
	var size int64
	for _, name := range []string{p, p + ".1"} {
		st, e := os.Stat(name)
		if e != nil {
			t.Fatal(e)
		}
		size += st.Size()
	}
	if size > 2*logSegment {
		t.Fatal(size)
	}
	b, _ := os.ReadFile(p)
	if !bytes.HasSuffix(b, []byte("last error")) {
		t.Fatal("lost failure tail")
	}
}
func TestPruneKeepsRecoveryAndIdempotency(t *testing.T) {
	m := &Manager{Dir: t.TempDir()}
	j := Job{Request: Request{ID: NewID()}, Status: "failed", UpdatedAt: time.Now().Add(-100 * 24 * time.Hour)}
	os.Mkdir(m.path(j.ID, ""), 0700)
	writeJSON(m.path(j.ID, "status.json"), j)
	for _, name := range []string{"worker.log", "agent.previous"} {
		os.WriteFile(m.path(j.ID, name), []byte("fixture"), 0600)
	}
	if e := m.pruneDiagnostics(); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(m.path(j.ID, "worker.log")); !os.IsNotExist(e) {
		t.Fatal("expired log retained")
	}
	if _, e := m.Get(j.ID); e != nil {
		t.Fatal("idempotency record removed")
	}
	if _, e := os.Stat(m.path(j.ID, "agent.previous")); e != nil {
		t.Fatal("recovery artifact removed")
	}
}

func TestLegacyOversizedLogsAreBoundedOnOpen(t *testing.T) {
	p := filepath.Join(t.TempDir(), "worker.log")
	for _, name := range []string{p, p + ".1"} {
		if e := os.WriteFile(name, append(bytes.Repeat([]byte("x"), logSegment*3), []byte("old tail")...), 0600); e != nil {
			t.Fatal(e)
		}
	}
	l, e := openLog(p)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = l.Write([]byte("new tail")); e != nil {
		t.Fatal(e)
	}
	l.Close()
	var size int64
	for _, name := range []string{p, p + ".1"} {
		st, e := os.Stat(name)
		if e != nil {
			t.Fatal(e)
		}
		size += st.Size()
	}
	if size > 2*logSegment {
		t.Fatal("legacy log escaped budget", size)
	}
	b, _ := os.ReadFile(p)
	if !bytes.HasSuffix(b, []byte("new tail")) {
		t.Fatal("lost latest error")
	}
}

func TestDiagnosticBudgetReservesNextWorker(t *testing.T) {
	m := &Manager{Dir: t.TempDir()}
	for i := 0; i < 17; i++ {
		j := Job{Request: Request{ID: NewID()}, Status: "succeeded", CreatedAt: time.Now().Add(time.Duration(i) * time.Second), UpdatedAt: time.Now()}
		os.Mkdir(m.path(j.ID, ""), 0700)
		writeJSON(m.path(j.ID, "status.json"), j)
		for _, name := range []string{"worker.log", "worker.log.1"} {
			f, e := os.Create(m.path(j.ID, name))
			if e != nil {
				t.Fatal(e)
			}
			if e = f.Truncate(logSegment); e != nil {
				t.Fatal(e)
			}
			f.Close()
		}
	}
	if e := m.pruneDiagnostics(); e != nil {
		t.Fatal(e)
	}
	var total int64
	for _, j := range m.List() {
		for _, name := range []string{"worker.log", "worker.log.1"} {
			if st, e := os.Stat(m.path(j.ID, name)); e == nil {
				total += st.Size()
			}
		}
	}
	if total+2*logSegment > 128<<20 {
		t.Fatal("next active worker exceeds aggregate budget", total)
	}
	if len(m.List()) != 17 {
		t.Fatal("idempotency records removed")
	}
}
