// Package agent implements ctlvps-agent: enrol, heartbeat, converge to the
// desired state and stream connection logs. Everything is outbound-only.
package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// State is persisted in <stateDir>/state.json.
type State struct {
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
	b, err := os.ReadFile(StatePath(dir))
	if err != nil {
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
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, StatePath(dir))
}
