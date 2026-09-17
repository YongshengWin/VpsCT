package maintenance

import (
	"os"
	"testing"
	"time"
)

func TestActiveActionAcrossHistoryPages(t *testing.T) {
	m := &Manager{Dir: t.TempDir()}
	for i := 0; i < 150; i++ {
		j := Job{Request: Request{ID: NewID(), Role: "agent", Action: "update"}, Status: "succeeded", UpdatedAt: time.Now()}
		if err := os.Mkdir(m.path(j.ID, ""), 0700); err != nil {
			t.Fatal(err)
		}
		if err := writeJSON(m.path(j.ID, "status.json"), j); err != nil {
			t.Fatal(err)
		}
	}
	active := Job{Request: Request{ID: NewID(), Role: "agent", Action: "uninstall"}, Status: "queued", UpdatedAt: time.Now()}
	if err := os.Mkdir(m.path(active.ID, ""), 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(m.path(active.ID, "status.json"), active); err != nil {
		t.Fatal(err)
	}
	m.Recover() // a fresh active task must remain queued
	if action, err := m.ActiveAction("agent"); err != nil || action != "uninstall" {
		t.Fatal(action, err)
	}
	if action, err := m.ActiveAction("controller"); err != nil || action != "" {
		t.Fatal(action, err)
	}
	visited := 0
	if err := m.visitJobs(func(Job) bool { visited++; return false }); err != nil || visited != 1 {
		t.Fatal("did not stop after first record", visited, err)
	}
}
