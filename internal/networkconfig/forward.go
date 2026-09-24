package networkconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"sort"
	"strings"
)

const ForwardVersion = 1
const ForwardHeader = "X-Ctlvps-Network-Forward-Version"

// Forward is a fixed-destination listener, never a client proxy node. Empty
// CIDRs in cidr mode deny every source; only explicit all mode opens admission.
type Forward struct {
	ListenMode        string   `json:"listen_mode"`
	ListenAddress     string   `json:"listen_address,omitempty"`
	ListenInterfaceID string   `json:"listen_interface_id,omitempty"`
	ListenPort        int      `json:"listen_port"`
	Network           string   `json:"network"` // tcp | udp | both
	TargetHost        string   `json:"target_host"`
	TargetPort        int      `json:"target_port"`
	SourceMode        string   `json:"source_mode"` // cidr | all
	SourceCIDRs       []string `json:"source_cidrs"`
	EgressProfileID   int64    `json:"egress_profile_id,omitempty"`
	EgressRevision    int64    `json:"egress_revision,omitempty"`
	MaxTCPConnections int      `json:"max_tcp_connections"`
	MaxUDPSessions    int      `json:"max_udp_sessions"`
	UDPIdleSeconds    int      `json:"udp_idle_seconds"`
}

// BindingPolicy adapts only interface selection to the shared local resolver.
// It does not give this resource a node ID, subscription address or share.
func (f Forward) BindingPolicy() Node {
	return Node{ListenMode: f.ListenMode, ListenAddress: f.ListenAddress, ListenInterfaceID: f.ListenInterfaceID,
		AdvertiseMode: "inherit", OnUnavailable: "block", EgressProfileID: f.EgressProfileID, EgressRevision: f.EgressRevision}
}

func (f Forward) Validate() error {
	if err := f.BindingPolicy().Validate(); err != nil {
		return err
	}
	if f.ListenPort < 1 || f.ListenPort > 65535 || f.TargetPort < 1 || f.TargetPort > 65535 {
		return errors.New("转发监听端口和目标端口须为 1–65535")
	}
	if err := ValidateAdvertiseHost(f.TargetHost); err != nil {
		return errors.New("转发目标须为单个 IP 或域名，不能包含协议、路径或端口")
	}
	if f.SourceMode != "cidr" && f.SourceMode != "all" {
		return errors.New("须明确选择来源地址段或允许任意来源")
	}
	if len(f.SourceCIDRs) > 64 || (f.SourceMode == "all" && len(f.SourceCIDRs) != 0) {
		return errors.New("来源地址段最多 64 条，任意来源模式不能同时指定地址段")
	}
	seen := map[netip.Prefix]bool{}
	for _, raw := range f.SourceCIDRs {
		p, err := netip.ParsePrefix(raw)
		if err != nil || p.Addr().Is4In6() || p != p.Masked() || p.Bits() == 0 || seen[p] {
			return errors.New("来源须为不重复的规范 CIDR；允许全部来源请显式选择任意来源")
		}
		seen[p] = true
	}
	tcp, udp := f.Network == "tcp" || f.Network == "both", f.Network == "udp" || f.Network == "both"
	if !tcp && !udp {
		return errors.New("转发传输类型须为 TCP、UDP 或两者")
	}
	if (tcp && (f.MaxTCPConnections < 1 || f.MaxTCPConnections > 4096)) || (!tcp && f.MaxTCPConnections != 0) {
		return errors.New("TCP 连接上限须为 1–4096；未启用 TCP 时须为零")
	}
	if (udp && (f.MaxUDPSessions < 1 || f.MaxUDPSessions > 4096 || f.UDPIdleSeconds < 2 || f.UDPIdleSeconds > 600)) || (!udp && (f.MaxUDPSessions != 0 || f.UDPIdleSeconds != 0)) {
		return errors.New("UDP 会话上限须为 1–4096、空闲超时须为 2–600 秒；未启用 UDP 时须为零")
	}
	return nil
}

func DecodeForward(raw []byte) (Forward, error) {
	var f Forward
	if len(raw) == 0 || len(raw) > 16384 {
		return f, errors.New("转发配置大小无效")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&f); err != nil || d.Decode(new(any)) != io.EOF {
		return Forward{}, errors.New("转发配置字段或类型无效")
	}
	if err := f.Validate(); err != nil {
		return Forward{}, err
	}
	if ip, err := netip.ParseAddr(f.TargetHost); err == nil {
		f.TargetHost = ip.String()
	} else {
		f.TargetHost = strings.ToLower(strings.TrimSuffix(f.TargetHost, "."))
	}
	if ip, err := netip.ParseAddr(f.ListenAddress); err == nil {
		f.ListenAddress = ip.String()
	}
	if f.SourceCIDRs == nil {
		f.SourceCIDRs = []string{}
	}
	for i, cidr := range f.SourceCIDRs {
		f.SourceCIDRs[i] = netip.MustParsePrefix(cidr).String()
	}
	sort.Strings(f.SourceCIDRs)
	return f, nil
}
