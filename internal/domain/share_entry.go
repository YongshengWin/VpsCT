package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"

	"ctlvps/internal/nodecombo"
)

// ShareEntry is one independently metered inbound on a share target. ID names an
// immutable combination within this share and server; NodeID is assigned by the store.
type ShareEntry struct {
	ID         string           `json:"id"`
	Snapshot   ShareEntryConfig `json:"snapshot"`
	QuotaBytes int64            `json:"quota_bytes"`
	NodeID     int64            `json:"node_id,omitempty"`
}

// Only credential-free protocol options belong in this snapshot. Endpoints,
// credentials and pinned network revisions remain on the dedicated node.
type ShareEntryConfig struct {
	Protocol       string            `json:"protocol"`
	VLESS          *nodecombo.VLESS  `json:"vless,omitempty"`
	Trojan         *nodecombo.Trojan `json:"trojan,omitempty"`
	Domain         string            `json:"domain,omitempty"` // TLS certificate name / SNI, independent of the WS Host.
	CertMode       string            `json:"cert_mode,omitempty"`
	CertID         string            `json:"cert_id,omitempty"` // Local policy alias, never private key material.
	SSMethod       string            `json:"ss_method,omitempty"`
	SSNetwork      string            `json:"ss_network,omitempty"`
	SnellVersion   int               `json:"snell_version,omitempty"`
	MieruTransport string            `json:"mieru_transport,omitempty"`
	Obfs           bool              `json:"obfs,omitempty"`
}

// ManagedTLS reports whether this entry uses one of the new ordinary TLS profiles.
func (c ShareEntryConfig) ManagedTLS() bool {
	return c.Protocol == ProtocolVLESS && c.VLESS != nil && c.VLESS.Security == "tls" ||
		c.Protocol == ProtocolTrojan && c.Trojan != nil && (c.Trojan.Transport == "ws" || c.Trojan.Transport == "grpc")
}

func (s *ShareEntry) UnmarshalJSON(b []byte) error {
	type wire ShareEntry
	var v wire
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return err
	}
	*s = ShareEntry(v)
	return nil
}

var shareEntryID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func (s ShareTarget) SelectedProtocols() []string {
	p := append([]string(nil), s.Protocols...)
	for _, e := range s.Entries {
		p = append(p, e.Snapshot.Protocol)
	}
	return p
}

func (s ShareTarget) ValidateEntries() error {
	seen := map[string]bool{}
	for _, e := range s.Entries {
		if !shareEntryID.MatchString(e.ID) || seen[e.ID] {
			return errors.New("组合入口 ID 只能为 1–64 位小写字母、数字、连字符或下划线，且在服务器内不能重复")
		}
		seen[e.ID] = true
		if e.QuotaBytes < 0 {
			return errors.New("节点配额不能为负数")
		}
		c := e.Snapshot
		if !ProtocolAllowed(c.Protocol, CoreModeStable) {
			return errors.New("组合协议不支持部署")
		}
		if !c.ManagedTLS() && (c.Domain != "" || c.CertMode != "" || c.CertID != "") {
			return errors.New("证书快照仅适用于受管 VLESS TLS、Trojan WS/gRPC + TLS 组合")
		}
		if c.VLESS != nil && c.Protocol != ProtocolVLESS || c.Trojan != nil && c.Protocol != ProtocolTrojan || (c.SSMethod != "" || c.SSNetwork != "") && c.Protocol != ProtocolShadowsocks || c.SnellVersion != 0 && c.Protocol != ProtocolSnell || c.MieruTransport != "" && c.Protocol != ProtocolMieru || c.Obfs && c.Protocol != ProtocolHysteria2 {
			return errors.New("配置快照包含不适用于所选协议的字段")
		}
	}
	return nil
}
