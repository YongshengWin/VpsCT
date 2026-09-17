// Package agent implements ctlvps-agent: enrol, heartbeat, converge to the
// desired state and stream connection logs. Everything is outbound-only.
package agent

import (
	"ctlvps/internal/agentproto"
	"ctlvps/internal/safehttp"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// State is persisted in <stateDir>/state.json.
type MeterIdentity struct {
	Generation string `json:"generation,omitempty"`
	NodeID     int64  `json:"node_id"`
	Port       int    `json:"port"`
	Core       string `json:"core"`
}

type State struct {
	Retirement        *Retirement           `json:"retirement,omitempty"`
	LegacySettled     bool                  `json:"legacy_settled,omitempty"`
	PendingSettlement *agentproto.Heartbeat `json:"pending_settlement,omitempty"`
	MeterNodes        []MeterIdentity       `json:"meter_nodes,omitempty"`

	MeteringV1      bool      `json:"metering_v1,omitempty"`
	ServerURL       string    `json:"server_url"`
	AgentToken      string    `json:"agent_token"`
	ServerID        int64     `json:"server_id"`
	ServerName      string    `json:"server_name"`
	PollIntervalSec int       `json:"poll_interval_sec"`
	AppliedRevision int64     `json:"applied_revision"`
	AppliedHash     string    `json:"applied_hash"`
	ApplyError      string    `json:"apply_error,omitempty"`
	CounterNonce    string    `json:"counter_nonce"` // changes whenever the nft table is recreated
	ConnlogSeq      int64     `json:"connlog_seq"`
	EnrolledAt      time.Time `json:"enrolled_at"`
}

// StatePath returns the state file path.
func StatePath(dir string) string { return filepath.Join(dir, "state.json") }

// LoadState reads the state file.
func LoadState(dir string) (*State, error) {
	f, err := os.Open(StatePath(dir))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := safehttp.ReadBounded(f, 4<<20)
	if err != nil {
		return nil, err
	}
	if err = safehttp.CheckJSONBudget(b); err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	if s.ServerURL == "" || s.AgentToken == "" {
		return nil, errors.New("state file incomplete; run `ctlvps-agent enroll` first")
	}
	return &s, nil
}

// Save writes the state atomically with restrictive permissions.
func (s *State) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := StatePath(dir) + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(tmp, StatePath(dir)); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Retained identities include unsettled retired nodes. Never discard them to
// satisfy a budget: block new identities until safe settlement is available.
const maxMeterIdentities = 2048

func (s *State) rememberMeters(nodes []agentproto.NodeSpec) error {
	known := make(map[int64]bool, len(s.MeterNodes))
	for _, n := range s.MeterNodes {
		known[n.NodeID] = true
	}
	additions := []MeterIdentity{}
	for _, n := range nodes {
		if !known[n.NodeID] {
			known[n.NodeID] = true
			additions = append(additions, MeterIdentity{NodeID: n.NodeID, Port: n.ListenPort, Core: n.Core, Generation: nonce()})
		}
	}
	if len(additions) > 0 && len(s.MeterNodes)+len(additions) > maxMeterIdentities {
		return errors.New("retained meter identity budget exhausted; settle retired nodes before adding new identities")
	}
	s.MeterNodes = append(s.MeterNodes, additions...)
	return nil
}
