package agentproto

import (
	"errors"

	"ctlvps/internal/networkconfig"
)

const NetworkForwardVersion = networkconfig.ForwardVersion
const NetworkForwardHeader = networkconfig.ForwardHeader

// ForwardSpec belongs to its own resource collection; it is never injected
// into Nodes or given a ShareID. Runtime observations cannot cross the wire.
type ForwardSpec struct {
	ForwardID      int64                        `json:"forward_id"`
	Revision       int64                        `json:"revision"`
	Config         networkconfig.Forward        `json:"config"`
	Direct         *networkconfig.Direct        `json:"direct,omitempty"`
	SOCKS5         *SOCKS5Egress                `json:"socks5,omitempty"`
	SS2022         *SS2022Egress                `json:"ss2022,omitempty"`
	SSH            *SSHEgress                   `json:"ssh,omitempty"`
	WireGuard      *WireGuardEgress             `json:"wireguard,omitempty"`
	Blocked        bool                         `json:"blocked"`
	Retired        bool                         `json:"retired,omitempty"`
	RuntimeNetwork *networkconfig.Resolved      `json:"-"`
	ForwardGrants  []networkconfig.ForwardGrant `json:"-"`
}

func (f ForwardSpec) Validate() error {
	if _, err := (ResourceIdentity{Kind: "forward", ID: f.ForwardID}).Mark(); err != nil {
		return err
	}
	if f.Revision < 1 || f.Revision > 4096 || (f.Retired && !f.Blocked) {
		return errors.New("转发版本或退役状态无效")
	}
	if err := f.Config.Validate(); err != nil {
		return err
	}
	count := 0
	for _, present := range []bool{f.Direct != nil, f.SOCKS5 != nil, f.SSH != nil, f.WireGuard != nil, f.SS2022 != nil} {
		if present {
			count++
		}
	}
	if count > 1 || (f.Config.EgressProfileID == 0) != (count == 0) {
		return errors.New("转发出口版本与配置不一致")
	}
	if f.SOCKS5 != nil {
		if err := f.SOCKS5.Config.Validate(); err != nil {
			return err
		}
		return f.SOCKS5.Credentials.Validate(f.SOCKS5.Config.Authentication)
	}
	if f.SS2022 != nil {
		if err := f.SS2022.Config.Validate(); err != nil {
			return err
		}
		return f.SS2022.Credentials.ValidateSS2022(f.SS2022.Config.Method)
	}
	if f.SSH != nil {
		if err := f.SSH.Config.Validate(); err != nil {
			return err
		}
		return f.SSH.Credentials.ValidateSSH(f.SSH.Config)
	}
	if f.WireGuard != nil {
		return f.WireGuard.Credentials.ValidateWireGuard(f.WireGuard.Config)
	}
	if f.Direct != nil {
		return f.Direct.Validate()
	}
	return nil
}

func (f ForwardSpec) HasTransport() bool {
	return f.SOCKS5 != nil || f.SSH != nil || f.WireGuard != nil || f.SS2022 != nil
}

func (f ForwardSpec) TransportConfig() (networkconfig.SOCKS5, bool) {
	if f.SS2022 != nil {
		return f.SS2022.Config.Transport(), true
	}
	if f.SOCKS5 != nil {
		return f.SOCKS5.Config, true
	}
	if f.SSH != nil {
		return f.SSH.Config.Transport(), true
	}
	if f.WireGuard != nil {
		return f.WireGuard.Config.Transport(), true
	}
	return networkconfig.SOCKS5{}, false
}

func (f ForwardSpec) OuterBinding() *networkconfig.Direct {
	if f.SS2022 != nil {
		return &f.SS2022.Config.Outer
	}
	if f.SOCKS5 != nil {
		return &f.SOCKS5.Config.Outer
	}
	if f.SSH != nil {
		return &f.SSH.Config.Outer
	}
	if f.WireGuard != nil {
		return &f.WireGuard.Config.Outer
	}
	return f.Direct
}

// ValidateTargetSelection admits only the configurations the agent can resolve.
// Runtime inventory, DNS answers and leases are checked locally before apply.
func (f ForwardSpec) ValidateTargetSelection() error {
	if err := f.Validate(); err != nil {
		return err
	}
	if f.HasTransport() {
		cfg, _ := f.TransportConfig()
		if f.Config.Network != "tcp" && (f.SSH != nil || !cfg.UDP) {
			return errors.New("UDP 固定转发需要支持 UDP 的中转出口")
		}
		ip, err := networkconfig.HostAddress(cfg.Server)
		if err != nil {
			return errors.New("固定转发中转端点须使用公网字面量 IP")
		}
		if err = (networkconfig.ResolvedSOCKS5{Purpose: cfg.Purpose, Address: ip.String(), Port: cfg.ServerPort, UDP: cfg.UDP}).Validate(); err != nil {
			return err
		}
		if _, err = networkconfig.HostAddress(f.Config.TargetHost); err != nil {
			return errors.New("中转出口的固定目标目前须使用字面量 IP")
		}
	}
	if _, err := networkconfig.HostAddress(f.Config.TargetHost); err == nil {
		ip, _ := networkconfig.HostAddress(f.Config.TargetHost)
		if f.Config.Network == "tcp" && networkconfig.NeedsTransportGrant(ip) {
			return nil
		} // root authorization is agent-local
		_, err = f.Config.LiteralTarget()
		return err
	}
	if f.Direct == nil {
		return errors.New("域名目标需要选择带 DNS 的直连出口")
	}
	// Reuse the public endpoint policy without any private transport grant.
	return (networkconfig.ResolvedSOCKS5{Address: f.Direct.DNS.Address, Port: f.Direct.DNS.Port}).Validate()
}
