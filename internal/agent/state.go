// Package agent implements ctlvps-agent: enrol, heartbeat, converge to the
// desired state and stream connection logs. Everything is outbound-only.
package agent

import (
	"ctlvps/internal/agentproto"
	"ctlvps/internal/safehttp"
	"encoding/hex"
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
	ListenBindingVersion    int                              `json:"listen_binding_version,omitempty"`
	ForwardDNSVersion       int                              `json:"forward_dns_version,omitempty"`
	ForwardPrivateVersion   int                              `json:"forward_private_version,omitempty"`
	ForwardTransportVersion int                              `json:"forward_transport_version,omitempty"`
	NetworkForwardVersion   int                              `json:"network_forward_version,omitempty"`
	ForwardNonce            string                           `json:"forward_nonce,omitempty"`
	ForwardMeters           []agentproto.ForwardMeterRule    `json:"forward_meters,omitempty"`
	ForwardPending          *forwardPending                  `json:"forward_pending,omitempty"`
	ForwardAckRevision      int64                            `json:"forward_ack_revision,omitempty"`
	NetworkBindingVersion   int                              `json:"network_binding_version,omitempty"`
	NetworkSSHVersion       int                              `json:"network_ssh_version,omitempty"`
	NetworkWireGuardVersion int                              `json:"network_wireguard_version,omitempty"`
	MitaVersion             int                              `json:"mita_version,omitempty"`
	NetworkEgressVersion    int                              `json:"network_egress_version,omitempty"`
	NetworkDesiredRevision  int64                            `json:"network_desired_revision,omitempty"`
	NetworkDesiredHash      string                           `json:"network_desired_hash,omitempty"`
	NetworkBillingPolicy    *agentproto.NetworkBillingPolicy `json:"network_billing_policy,omitempty"`
	NetworkBillingRequested *agentproto.NetworkBillingPolicy `json:"network_billing_requested,omitempty"`
	NetworkBillingPending   *agentproto.NetworkBillingSwitch `json:"network_billing_pending,omitempty"`
	Retirement              *Retirement                      `json:"retirement,omitempty"`
	LegacySettled           bool                             `json:"legacy_settled,omitempty"`
	PendingSettlement       *agentproto.Heartbeat            `json:"pending_settlement,omitempty"`
	MeterNodes              []MeterIdentity                  `json:"meter_nodes,omitempty"`

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
	if s.NetworkDesiredRevision < 0 || (s.NetworkDesiredRevision == 0) != (s.NetworkDesiredHash == "") {
		return nil, errors.New("network desired-state revision is invalid")
	}
	if s.NetworkDesiredRevision > 0 {
		digest, err := hex.DecodeString(s.NetworkDesiredHash)
		if err != nil || len(digest) != 32 {
			return nil, errors.New("network desired-state digest is invalid")
		}
		// Pre-release binding agents persisted intent before an explicit
		// version field existed. They already require the binding contract.
		if s.NetworkBindingVersion == 0 {
			s.NetworkBindingVersion = agentproto.NetworkBindingVersion
		}
	}
	if s.NetworkBindingVersion != 0 && s.NetworkBindingVersion != agentproto.NetworkBindingVersion {
		return nil, errors.New("unsupported persisted network binding version")
	}
	for _, version := range []int{s.ListenBindingVersion, s.ForwardDNSVersion, s.ForwardPrivateVersion, s.ForwardTransportVersion} {
		if version < 0 || version > 1 {
			return nil, errors.New("unsupported persisted network feature version")
		}
	}
	if s.NetworkEgressVersion != 0 && (s.NetworkEgressVersion != agentproto.NetworkEgressVersion || s.NetworkBindingVersion != agentproto.NetworkBindingVersion) {
		return nil, errors.New("unsupported persisted network egress version")
	}
	if s.NetworkWireGuardVersion != 0 && (s.NetworkWireGuardVersion != agentproto.NetworkWireGuardVersion || s.NetworkEgressVersion != agentproto.NetworkEgressVersion) {
		return nil, errors.New("unsupported persisted WireGuard version")
	}
	if s.MitaVersion != 0 && s.MitaVersion != 1 {
		return nil, errors.New("unsupported persisted mita capability")
	}
	if s.NetworkSSHVersion != 0 && (s.NetworkSSHVersion != agentproto.NetworkSSHVersion || s.NetworkEgressVersion != agentproto.NetworkEgressVersion) {
		return nil, errors.New("unsupported persisted SSH version")
	}
	if s.NetworkForwardVersion != 0 && (s.NetworkForwardVersion != agentproto.NetworkForwardVersion || s.NetworkBindingVersion != agentproto.NetworkBindingVersion) {
		return nil, errors.New("unsupported persisted forwarding version")
	}
	if s.ForwardPending != nil {
		if err := s.ForwardPending.Receipt.Validate(); err != nil {
			return nil, err
		}
	}
	return &s, nil
}

func (s *State) desiredBoundary() (int64, string) {
	if s.NetworkDesiredRevision >= s.AppliedRevision && s.NetworkDesiredRevision > 0 {
		return s.NetworkDesiredRevision, s.NetworkDesiredHash
	}
	return s.AppliedRevision, s.AppliedHash
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
