package secureupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"time"

	"ctlvps/internal/networkconfig"
	"ctlvps/internal/safehttp"
)

type agentRequirements struct {
	ForwardUDP       int             `json:"forward_udp_version"`
	OpenRCMieru      int             `json:"openrc_mieru_version"`
	OpenRC           int             `json:"openrc_version"`
	NAT              int             `json:"nat_version"`
	ListenBinding    int             `json:"listen_binding_version"`
	ForwardDNS       int             `json:"forward_dns_version"`
	ForwardPrivate   int             `json:"forward_private_version"`
	ForwardTransport int             `json:"forward_transport_version"`
	Mita             int             `json:"mita_version"`
	WireGuard        int             `json:"network_wireguard_version"`
	SSH              int             `json:"network_ssh_version"`
	Binding          int             `json:"network_binding_version"`
	Egress           int             `json:"network_egress_version"`
	Forward          int             `json:"network_forward_version"`
	Revision         int64           `json:"network_desired_revision"`
	Billing          json.RawMessage `json:"network_billing_policy"`
	BillingRequested json.RawMessage `json:"network_billing_requested"`
	BillingPending   json.RawMessage `json:"network_billing_pending"`
}

func readAgentRequirements(path string) (agentRequirements, error) {
	var r agentRequirements
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return r, nil
	}
	if err != nil {
		return r, errors.New("无法检查 agent 本地兼容状态")
	}
	defer f.Close()
	raw, err := safehttp.ReadBounded(f, 8<<20)
	if err != nil || json.Unmarshal(raw, &r) != nil || r.Mita < 0 || r.WireGuard < 0 || r.SSH < 0 || r.Binding < 0 || r.Egress < 0 || r.Forward < 0 || r.Revision < 0 {
		return r, errors.New("agent 本地兼容状态无效")
	}
	if r.SSH > 0 || r.WireGuard > 0 {
		r.Egress = max(1, r.Egress)
	}
	if r.ListenBinding < 0 || r.ForwardDNS < 0 || r.ForwardPrivate < 0 || r.ForwardTransport < 0 {
		return r, errors.New("agent 网络特性状态无效")
	}
	if r.NAT < 0 || r.NAT > networkconfig.NATVersion {
		return r, errors.New("agent NAT 特性状态无效")
	}
	if r.OpenRCMieru < 0 || r.OpenRCMieru > 1 || r.OpenRC < 0 || r.OpenRC > 1 {
		return r, errors.New("agent OpenRC 特性状态无效")
	}
	if r.ForwardUDP < 0 || r.ForwardUDP > networkconfig.ForwardUDPVersion {
		return r, errors.New("agent UDP 会话管理状态无效")
	}
	if r.ForwardDNS > 0 || r.ForwardPrivate > 0 || r.ForwardTransport > 0 || r.ForwardUDP > 0 {
		r.Forward = max(1, r.Forward)
	}
	if r.ForwardTransport > 0 {
		r.Egress = max(1, r.Egress)
	}
	if r.ListenBinding > 0 {
		r.Binding = max(1, r.Binding)
	}
	if r.Revision > 0 || r.Egress > 0 || r.Forward > 0 {
		r.Binding = max(1, r.Binding)
	}
	return r, nil
}

func (r agentRequirements) billingRequired() bool {
	for _, v := range []json.RawMessage{r.Billing, r.BillingRequested, r.BillingPending} {
		if len(v) > 0 && string(v) != "null" {
			return true
		}
	}
	return false
}

func (r agentRequirements) check(c networkconfig.ExecutableCapabilities) error {
	// Preserve the legacy base capability so an already-installed v1 updater
	// can inspect this candidate. The separate flag proves v2 isolation support.
	udpVersion := c.ForwardUDP
	if c.ForwardUDPIsolation < 0 || c.ForwardUDPIsolation > 1 || c.ForwardUDPIsolation > 0 && c.ForwardUDP < 1 {
		return errors.New("候选 UDP 隔离能力无效")
	}
	if c.ForwardUDPIsolation == 1 {
		udpVersion = max(udpVersion, networkconfig.ForwardUDPVersion)
	}

	if c.ForwardUDP > networkconfig.ForwardUDPVersion || r.ForwardUDP < 0 || r.ForwardUDP > networkconfig.ForwardUDPVersion || (r.ForwardUDP > 0 && udpVersion < r.ForwardUDP) {
		return errors.New("候选 agent 不支持本机 UDP 会话管理，拒绝降级")
	}
	if r.OpenRCMieru > 0 && r.OpenRCMieru != c.OpenRCMieru {
		return errors.New("候选 agent 不支持本机 OpenRC mieru 计量，拒绝降级")
	}
	if r.OpenRC > 0 && r.OpenRC != c.OpenRC {
		return errors.New("候选 agent 不支持本机 OpenRC 服务管理，拒绝降级；原程序保持不变")
	}
	if r.NAT > 0 && c.NAT < r.NAT {
		return errors.New("候选 agent 不支持已启用的 NAT 端口适配，拒绝降级")
	}
	if r.ListenBinding > 0 && r.ListenBinding != c.ListenBinding || r.ForwardDNS > 0 && r.ForwardDNS != c.ForwardDNS || r.ForwardPrivate > 0 && r.ForwardPrivate != c.ForwardPrivate || r.ForwardTransport > 0 && r.ForwardTransport != c.ForwardTransport {
		return errors.New("候选 agent 不支持已启用的监听或固定转发能力，拒绝降级")
	}
	if c.Schema != 1 || r.Mita > 0 && c.Mita != r.Mita || (r.WireGuard > 0 && c.NetworkWireGuard != r.WireGuard) || (r.SSH > 0 && c.NetworkSSH != r.SSH) || (r.Binding > 0 && c.NetworkBinding != r.Binding) || (r.Egress > 0 && c.NetworkEgress != r.Egress) || (r.Forward > 0 && c.NetworkForward != r.Forward) || (r.billingRequired() && c.NetworkBilling != networkconfig.BillingVersion) {
		return errors.New("候选 agent 不支持本机已启用的网络或计量协议，拒绝降级；原程序保持不变")
	}
	return nil
}

type capabilityOutput struct{ bytes.Buffer }

func (b *capabilityOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4096 {
		return 0, errors.New("agent capability output exceeds limit")
	}
	return b.Buffer.Write(p)
}

// CheckAgentCompatibility only executes a binary AFTER the caller verified
// its release signature and digest, while holding the configuration lock.
// It never outputs local state (which also contains the enrollment credential).
func CheckAgentCompatibility(ctx context.Context, binary, statePath string) error {
	r, err := readAgentRequirements(statePath)
	if err != nil {
		return err
	}
	if r.OpenRC == 0 && r.NAT == 0 && r.Mita == 0 && r.Binding == 0 && r.Egress == 0 && r.Forward == 0 && r.ForwardUDP == 0 && !r.billingRequired() {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "capabilities")
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	var out capabilityOutput
	cmd.Stdout, cmd.Stderr = &out, io.Discard
	if err = cmd.Run(); err != nil {
		return errors.New("候选 agent 缺少网络兼容能力声明，原程序未更换")
	}
	var c networkconfig.ExecutableCapabilities
	if json.Unmarshal(out.Bytes(), &c) != nil {
		return errors.New("候选 agent 网络兼容能力声明无效，原程序未更换")
	}
	return r.check(c)
}
