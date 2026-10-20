package agent

import (
	"testing"
	"time"
)

func TestApplyFailureLoggingDoesNotHideChangedFailure(t *testing.T) {
	var f applyFailureLog
	now := time.Unix(1000, 0)
	if !f.record(1, "a", "firewall", now) {
		t.Fatal("first failure hidden")
	}
	if f.record(1, "a", "firewall", now.Add(30*time.Second)) {
		t.Fatal("duplicate not suppressed")
	}
	if !f.record(1, "a", "firewall", now.Add(5*time.Minute)) {
		t.Fatal("periodic reminder hidden")
	}
	if !f.record(1, "a", "start", now.Add(301*time.Second)) {
		t.Fatal("changed failure hidden")
	}
	if !f.record(2, "b", "start", now.Add(302*time.Second)) {
		t.Fatal("new desired state hidden")
	}
	f = applyFailureLog{}
	if !f.record(2, "b", "start", now.Add(303*time.Second)) {
		t.Fatal("failure after recovery hidden")
	}
}
