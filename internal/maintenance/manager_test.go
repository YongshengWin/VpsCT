package maintenance

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRequestScope(t *testing.T) {
	base := Request{ID: NewID(), Role: "controller", Action: "update", Version: "v1.2.3"}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Request){
		func(r *Request) { r.ID = "../escape" }, func(r *Request) { r.Role = "all" }, func(r *Request) { r.Action = "shell" },
		func(r *Request) { r.Version = "latest" }, func(r *Request) { r.Version = "v1.2.3; id" }, func(r *Request) { r.Purge = true },
		func(r *Request) { r.Action = "uninstall"; r.Version = ""; r.RemoveCaddy = true },
		func(r *Request) {
			r.Role = "agent"
			r.Action = "uninstall"
			r.Version = ""
			r.Purge = true
			r.RemoveCaddy = true
		},
	} {
		r := base
		mutate(&r)
		if r.Validate() == nil {
			t.Errorf("accepted invalid request: %+v", r)
		}
	}
}

func TestDurableIdempotentAdmission(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(t.TempDir(), "executable")
	if err := os.WriteFile(exe, []byte("test fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	var launches atomic.Int32
	m := &Manager{Dir: dir, Executable: exe, Launch: func(string, string) error { launches.Add(1); return nil }}
	s := Spec{Request: Request{ID: NewID(), Role: "agent", Action: "uninstall"}}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.Start(s); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if launches.Load() != 1 {
		t.Fatalf("launched %d times", launches.Load())
	}
	// A new daemon instance reads the same job rather than running it again.
	m2 := &Manager{Dir: dir, Executable: exe, Launch: m.Launch}
	if _, err := m2.Start(s); err != nil {
		t.Fatal(err)
	}
	other := s
	other.ID = NewID()
	other.Role = "controller"
	if _, err := m2.Start(other); err == nil {
		t.Fatal("concurrent role accepted")
	}
	changed := s
	changed.Purge = true
	if _, err := m.Start(changed); err == nil {
		t.Fatal("same ID allowed different deletion scope")
	}
	j, err := m.Get(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	j.Status = "succeeded"
	j.UpdatedAt = time.Now().UTC()
	if err := writeJSON(m.path(s.ID, "status.json"), j); err != nil {
		t.Fatal(err)
	}
	if _, err := m2.Start(other); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(m.path(s.ID, "request.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("request permissions")
	}
	if _, err := m.Get("../../escape"); err == nil {
		t.Fatal("path traversal accepted")
	}
}
