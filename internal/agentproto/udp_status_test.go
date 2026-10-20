package agentproto

import "testing"

func TestUDPDiagnosticCountersAndReasonsAreBounded(t *testing.T) {
	valid := ForwardUDPStatus{State: "healthy", Active: map[int64]int{1: 1024}, Reaped: 40, SweepMillis: 30}
	if err := ValidateNetworkDiagnostics(Diagnostics{ForwardUDP: &valid}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []ForwardUDPStatus{{State: "secret"}, {State: "paused", Code: "raw error with password"}, {State: "healthy", Active: map[int64]int{1: 8193}}, {State: "healthy", Reaped: 1 << 53}, {State: "healthy", SweepMillis: -1}} {
		if ValidateNetworkDiagnostics(Diagnostics{ForwardUDP: &bad}) == nil {
			t.Fatal("invalid UDP diagnostics accepted", bad.State)
		}
	}
}
