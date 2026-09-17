// Package agentproto defines the JSON contract between ctlvpsd and
// ctlvps-agent. All traffic is agent -> server (outbound only).
package agentproto

import (
	"ctlvps/internal/maintenance"
	"time"
)

// Header carrying the agent token.
const AuthHeader = "Authorization"

// API paths (relative to the server base URL).
const (
	PathEnroll      = "/api/agent/v1/enroll"
	PathHeartbeat   = "/api/agent/v1/heartbeat"
	PathDesired     = "/api/agent/v1/desired"
	PathApplyReport = "/api/agent/v1/apply-report"
	PathConnlog     = "/api/agent/v1/connlog"
)

// EnrollRequest exchanges a one-time token for a permanent one.
type EnrollRequest struct {
	EnrollToken string `json:"enroll_token"`
	Version     string `json:"version"`
	Hostname    string `json:"hostname"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	Kernel      string `json:"kernel"`
}

// EnrollResponse is returned once; the agent must persist AgentToken.
type EnrollResponse struct {
	AgentToken      string `json:"agent_token"`
	ServerID        int64  `json:"server_id"`
	ServerName      string `json:"server_name"`
	PollIntervalSec int    `json:"poll_interval_sec"`
}

// Metrics is a system snapshot.
type Metrics struct {
	CPUPercent float64 `json:"cpu_percent"`
	Load1      float64 `json:"load1"`
	Load5      float64 `json:"load5"`
	MemTotal   int64   `json:"mem_total"`
	MemUsed    int64   `json:"mem_used"`
	SwapTotal  int64   `json:"swap_total"`
	SwapUsed   int64   `json:"swap_used"`
	DiskTotal  int64   `json:"disk_total"`
	DiskUsed   int64   `json:"disk_used"`
	UptimeSec  int64   `json:"uptime_sec"`
	NetRx      int64   `json:"net_rx"` // cumulative bytes received on public NICs
	NetTx      int64   `json:"net_tx"` // cumulative bytes sent
	NetRxRate  int64   `json:"net_rx_rate"`
	NetTxRate  int64   `json:"net_tx_rate"`
	TCPConns   int     `json:"tcp_conns"`
	UDPConns   int     `json:"udp_conns"`
	Processes  int     `json:"processes"`
	Hostname   string  `json:"hostname"`
	Kernel     string  `json:"kernel"`
	Arch       string  `json:"arch"`
	Interface  string  `json:"interface"`
}

// PortCounter is a cumulative nftables counter for one listening port.
type PortCounter struct {
	NodeID   int64  `json:"node_id,omitempty"`
	Source   string `json:"source,omitempty"`
	Epoch    string `json:"epoch,omitempty"`
	FromZero bool   `json:"from_zero,omitempty"`
	Port     int    `json:"port"`
	Rx       int64  `json:"rx"` // VPS inbound for this inbound (client + origin)
	Tx       int64  `json:"tx"` // VPS outbound for this inbound (client + origin)
	RxPkts   int64  `json:"rx_pkts,omitempty"`
	TxPkts   int64  `json:"tx_pkts,omitempty"`
}

// CoreStatus describes one proxy core process.
type CoreStatus struct {
	Name       string    `json:"name"` // sing-box | snell-server
	Version    string    `json:"version"`
	Installed  bool      `json:"installed"`
	Active     bool      `json:"active"`
	Wanted     bool      `json:"wanted"` // should be running per desired state
	RSSBytes   int64     `json:"rss_bytes"`
	CPUPercent float64   `json:"cpu_percent"`
	NRestarts  int       `json:"nrestarts"`
	Since      time.Time `json:"since,omitempty"`
	LastError  string    `json:"last_error,omitempty"`
	Instances  int       `json:"instances,omitempty"` // one process per listen port
}

// CertStatus describes a TLS certificate in use.
type CertStatus struct {
	Domain   string    `json:"domain"`
	Mode     string    `json:"mode"`
	NotAfter time.Time `json:"not_after"`
	Issuer   string    `json:"issuer"`
}

// Diagnostics is the health section of a heartbeat.
type Diagnostics struct {
	SecurityVersion int          `json:"security_version,omitempty"`
	SecurityPolicy  bool         `json:"security_policy,omitempty"`
	SecurityPaused  bool         `json:"security_paused,omitempty"`
	MeteringError   string       `json:"metering_error,omitempty"`
	Maintenance     int          `json:"maintenance,omitempty"`
	Cores           []CoreStatus `json:"cores"`
	Certs           []CertStatus `json:"certs,omitempty"`
	ClockSkewMs     int64        `json:"clock_skew_ms"`
	BBR             bool         `json:"bbr"`
	CongestionCtl   string       `json:"congestion_ctl"`
	IPv6Reachable   bool         `json:"ipv6_reachable"`
	IPv4Reachable   bool         `json:"ipv4_reachable"`
	OOMEvents       int          `json:"oom_events"`
	Nftables        bool         `json:"nftables"`
	Systemd         bool         `json:"systemd"`
	TimeSync        bool         `json:"time_sync"`
	Warnings        []string     `json:"warnings,omitempty"`
	RecentErrors    []string     `json:"recent_errors,omitempty"`
	ConnlogLag      int64        `json:"connlog_lag"` // buffered events not yet uploaded
	BinarySHA256    string       `json:"binary_sha256,omitempty"`
}

// Heartbeat is sent every poll interval.
type Heartbeat struct {
	FinalMeters     *MeterSettlement `json:"final_meters,omitempty"`
	Version         string           `json:"version"`
	BinarySHA256    string           `json:"binary_sha256,omitempty"`
	Epoch           string           `json:"epoch"` // changes when counters reset; ingest rebases if NIC did not reset
	TS              time.Time        `json:"ts"`
	PublicIPv4      string           `json:"public_ipv4"`
	PublicIPv6      string           `json:"public_ipv6"`
	Metrics         Metrics          `json:"metrics"`
	Ports           []PortCounter    `json:"ports"`
	AppliedRevision int64            `json:"applied_revision"`
	AppliedHash     string           `json:"applied_hash"`
	ApplyStatus     string           `json:"apply_status"` // applied|failed|pending
	ApplyError      string           `json:"apply_error,omitempty"`
	Diagnostics     Diagnostics      `json:"diagnostics"`
}

// HeartbeatResponse tells the agent what to do next.
type HeartbeatResponse struct {
	FinalMeterVersion int                 `json:"final_meter_version,omitempty"`
	FinalMeterAck     string              `json:"final_meter_ack,omitempty"`
	MeteringVersion   int                 `json:"metering_version,omitempty"`
	Maintenance       *MaintenanceCommand `json:"maintenance,omitempty"`
	ServerTime        time.Time           `json:"server_time"`
	DesiredRevision   int64               `json:"desired_revision"`
	DesiredHash       string              `json:"desired_hash"`
	PollIntervalSec   int                 `json:"poll_interval_sec"`
	ConnlogEnabled    bool                `json:"connlog_enabled"`
	CounterReset      bool                `json:"counter_reset"` // server lost baseline; agent may reset
	AgentUpdate       *AgentUpdateSpec    `json:"agent_update,omitempty"`
}

type MaintenanceCommand struct {
	Request     maintenance.Request `json:"request"`
	ReportToken string              `json:"report_token"`
	SHA256      string              `json:"sha256,omitempty"`
}

// AgentUpdateSpec tells an agent to replace its own binary.
type AgentUpdateSpec struct {
	SHA256 string `json:"sha256"`
	URL    string `json:"url"`
	Size   int64  `json:"size,omitempty"`
}

// CertSpec tells the agent how to obtain the TLS certificate for a node.
type CertSpec struct {
	ID       string `json:"id,omitempty"` // local certificate registration, never an arbitrary path
	Mode     string `json:"mode"`         // self_signed | acme | external
	Domain   string `json:"domain"`
	CertPath string `json:"cert_path,omitempty"` // external
	KeyPath  string `json:"key_path,omitempty"`
	Email    string `json:"email,omitempty"`
}

// NodeSpec is one inbound the agent must serve.
type NodeSpec struct {
	AllowPrivate   bool           `json:"-"` // resolved only from root-owned local policy
	Retired        bool           `json:"-"` // local accounting tombstone, never an inbound
	NodeID         int64          `json:"node_id"`
	Name           string         `json:"name"`
	Protocol       string         `json:"protocol"`
	Core           string         `json:"core"` // singbox | snell
	ListenPort     int            `json:"listen_port"`
	ShareID        *int64         `json:"share_id,omitempty"`
	Blocked        bool           `json:"blocked"`
	ConnlogEnabled bool           `json:"connlog_enabled"`
	Params         map[string]any `json:"params"` // server-side protocol params
	Cert           *CertSpec      `json:"cert,omitempty"`
}

// CoreVersion pins a downloadable core binary.
type CoreVersion struct {
	Version string            `json:"version"`
	URL     string            `json:"url"`              // template with {version} {arch}
	SHA256  map[string]string `json:"sha256,omitempty"` // arch -> hex
}

// DesiredState is the full declarative state for one server.
type DesiredState struct {
	Revision    int64                  `json:"revision"`
	Hash        string                 `json:"hash"`
	ServerID    int64                  `json:"server_id"`
	ServerName  string                 `json:"server_name"`
	PublicHost  string                 `json:"public_host"`
	CoreMode    string                 `json:"core_mode"`
	IPv4Only    bool                   `json:"ipv4_only"`
	Nodes       []NodeSpec             `json:"nodes"`
	Versions    map[string]CoreVersion `json:"versions"`
	Connlog     ConnlogSpec            `json:"connlog"`
	Tuning      Tuning                 `json:"tuning"`
	GeneratedAt time.Time              `json:"generated_at"`
}

// ConnlogSpec controls connection log collection.
type ConnlogSpec struct {
	Enabled     bool `json:"enabled"`
	BatchSize   int  `json:"batch_size"`
	FlushSec    int  `json:"flush_sec"`
	MaxBufferMB int  `json:"max_buffer_mb"`
}

// Tuning are host-level knobs applied idempotently.
type Tuning struct {
	EnableBBR    bool `json:"enable_bbr"`
	MemoryMaxMB  int  `json:"memory_max_mb"` // aggregate proxy slice budget, also an individual safety ceiling
	LimitNOFILE  int  `json:"limit_nofile"`
	Chrony       bool `json:"chrony"`
	RestartSec   int  `json:"restart_sec"`
	GoMemLimitMB int  `json:"gomemlimit_mb"`
}

// ApplyReport is sent after the agent reconciles a revision.
type ApplyReport struct {
	Revision int64    `json:"revision"`
	Hash     string   `json:"hash"`
	Status   string   `json:"status"` // applied|failed
	Error    string   `json:"error,omitempty"`
	Details  []string `json:"details,omitempty"`
}

// ConnEvent is one observed connection.
type ConnEvent struct {
	TS       time.Time `json:"ts"`
	NodeID   int64     `json:"node_id"`
	Network  string    `json:"network"`
	DestHost string    `json:"dest_host"`
	DestPort int       `json:"dest_port"`
	SrcHost  string    `json:"src_host,omitempty"`
}

// ConnlogBatch uploads buffered events; Seq is monotonically increasing per
// agent and used for idempotent acknowledgement.
type ConnlogBatch struct {
	Seq    int64       `json:"seq"`
	Events []ConnEvent `json:"events"`
}

// ConnlogAck acknowledges a batch.
type ConnlogAck struct {
	AcceptedSeq int64 `json:"accepted_seq"`
	Enabled     bool  `json:"enabled"`
}

// Default poll interval.
const DefaultPollIntervalSec = 30
