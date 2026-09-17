package agent

import (
	"ctlvps/internal/agentproto"
	"os"
	"strings"
	"testing"
)

func TestMeterBudgetDoesNotLoseUnsettledState(t *testing.T) {
	s := &State{}
	for i := 1; i <= maxMeterIdentities; i++ {
		s.MeterNodes = append(s.MeterNodes, MeterIdentity{NodeID: int64(i)})
	}
	err := s.rememberMeters([]agentproto.NodeSpec{{NodeID: 1}, {NodeID: 99999}})
	if err == nil || len(s.MeterNodes) != maxMeterIdentities {
		t.Fatal("growth budget or atomicity broken")
	}
	if err = s.rememberMeters([]agentproto.NodeSpec{{NodeID: 1}}); err != nil {
		t.Fatal("existing nodes must remain operable", err)
	}
	s = &State{}
	if err = s.rememberMeters([]agentproto.NodeSpec{{NodeID: 1}, {NodeID: 1}}); err != nil || len(s.MeterNodes) != 1 {
		t.Fatal("duplicate retained identity")
	}
}
func TestOversizedStateRejected(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(StatePath(dir), []byte(strings.Repeat(" ", 4<<20+1)), 0600)
	if _, err := LoadState(dir); err == nil {
		t.Fatal("oversized state accepted")
	}
}
