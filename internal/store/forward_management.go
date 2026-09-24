package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/corecompat"
	"ctlvps/internal/domain"
	"ctlvps/internal/netinventory"
	"ctlvps/internal/networkconfig"
)

var ErrForwardRequest = errors.New("转发请求无效")

func (s *Store) PreviewPortForward(ctx context.Context, in PortForwardRequest) (NetworkReadiness, error) {
	in.ID, in.ExpectedImpact = "00000000000000000000000000000000", ""
	if _, err := in.normalize(); err != nil {
		return NetworkReadiness{}, fmt.Errorf("%w: %w", ErrForwardRequest, err)
	}
	var view NetworkReadiness
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		f := domain.PortForward{ServerID: in.ServerID}
		var err error
		if in.Action != "create" {
			f, err = scanForward(tx.QueryRowContext(ctx, `SELECT `+forwardCols+` FROM port_forwards WHERE id=?`, in.ForwardID))
			if err != nil {
				return err
			}
			if f.Revision != in.ExpectedRevision {
				return ErrNetworkConflict
			}
		}
		view, err = s.previewPortForward(ctx, tx, f, in)
		return err
	})
	return view, err
}

func (s *Store) previewPortForward(ctx context.Context, q querier, f domain.PortForward, in PortForwardRequest) (NetworkReadiness, error) {
	state, err := s.networkReadiness(ctx, q, f.ServerID)
	if err != nil {
		return state.view, err
	}
	v := &state.view
	// Stopping a listener must remain possible after an interface or target
	// disappears. The agent still needs permission to apply and report cleanup.
	if in.Action == "delete" || !in.Enabled {
		v.Ready = true
		checks := v.Checks
		v.Checks = nil
		for _, c := range checks {
			switch c.Code {
			case "agent_online", "binding_protocol", "egress_protocol", "forward_protocol", "local_policy", "maintenance":
				v.check(c.Code, c.Ready, c.Message)
			}
		}
	}
	forwardChecked := false
	for _, c := range v.Checks {
		forwardChecked = forwardChecked || c.Code == "forward_protocol"
	}
	if !forwardChecked {
		v.check("forward_protocol", v.ForwardVersion == agentproto.NetworkForwardVersion, "agent 需要支持固定转发及清理回执")
	}
	v.check("forward_editable", !f.Retired, "转发已进入清理，不能再次编辑")
	if in.Action != "delete" {
		candidate := f
		candidate.Config = *in.Config
		if in.Enabled && candidate.Config.Network != "tcp" {
			var pinned string
			err := q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, domain.SettingSingBoxVersion).Scan(s.scanSecret("settings.value", &pinned))
			if err != nil && !isNoRows(err) {
				return *v, err
			}
			v.check("forward_udp_core", corecompat.UDPForward(pinned), corecompat.UDPForwardUnavailable)
		}
		if err = checkForwardEgress(ctx, q, candidate); err != nil {
			return *v, fmt.Errorf("%w: %s", ErrForwardRequest, err)
		}
		var direct *networkconfig.Direct
		var transport *networkconfig.SOCKS5
		if candidate.Config.EgressProfileID != 0 {
			var raw, kind string
			var enabled bool
			err = q.QueryRowContext(ctx, `SELECT r.config,p.enabled,p.kind FROM egress_profile_revisions r JOIN egress_profiles p ON p.id=r.profile_id WHERE r.profile_id=? AND r.revision=? AND r.server_id=?`, candidate.Config.EgressProfileID, candidate.Config.EgressRevision, f.ServerID).Scan(&raw, &enabled, &kind)
			if err != nil {
				return *v, err
			}
			switch kind {
			case "direct":
				d, decodeErr := networkconfig.DecodeDirect([]byte(raw))
				if decodeErr != nil {
					return *v, decodeErr
				}
				direct = &d
			case "socks5", "ssh", "wireguard", "ss2022":
				var cfg networkconfig.SOCKS5
				if kind == "socks5" {
					decoded, decodeErr := networkconfig.DecodeSOCKS5([]byte(raw))
					if decodeErr != nil {
						return *v, decodeErr
					}
					cfg = decoded
				} else if kind == "ss2022" {
					decoded, decodeErr := networkconfig.DecodeSS2022([]byte(raw))
					if decodeErr != nil {
						return *v, decodeErr
					}
					cfg = decoded.Transport()
				} else if kind == "ssh" {
					decoded, decodeErr := networkconfig.DecodeSSH([]byte(raw))
					if decodeErr != nil {
						return *v, decodeErr
					}
					cfg = decoded.Transport()
				} else {
					decoded, decodeErr := networkconfig.DecodeWireGuard([]byte(raw))
					if decodeErr != nil {
						return *v, decodeErr
					}
					cfg = decoded.Transport()
				}
				transport = &cfg
				direct = &cfg.Outer
			default:
				return *v, ErrForwardRequest
			}
			if in.Enabled {
				v.check("egress_enabled", enabled, "所选出口配置已停用")
				if transport != nil {
					if kind == "ss2022" {
						var pinned string
						err := q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, domain.SettingSingBoxVersion).Scan(s.scanSecret("settings.value", &pinned))
						if err != nil && !isNoRows(err) {
							return *v, err
						}
						v.check("ss2022_core", corecompat.SS2022Outbound(pinned), corecompat.SS2022Requirement)
					}
					if candidate.Config.Network != "tcp" {
						v.check("forward_udp_transport", kind != "ssh" && transport.UDP, "所选中转出口不支持 UDP")
					}
					v.check("forward_transport", v.ForwardTransportVersion == 1 && v.EgressVersion == agentproto.NetworkEgressVersion, "agent 需要支持固定转发中转出口")
					if kind == "ssh" {
						v.check("ssh_protocol", v.SSHVersion == agentproto.NetworkSSHVersion, "agent 需要支持 SSH 中转")
					}
					if kind == "wireguard" {
						v.check("wireguard_protocol", v.WireGuardVersion == agentproto.NetworkWireGuardVersion, "agent 需要支持 WireGuard 中转")
					}
					ip, ipErr := networkconfig.HostAddress(transport.Server)
					endpointErr := ipErr
					if endpointErr == nil {
						endpointErr = netinventory.ValidateSOCKS5Endpoint(networkconfig.ResolvedSOCKS5{Purpose: transport.Purpose, Address: ip.String(), Port: transport.ServerPort, UDP: transport.UDP}, state.inventory)
					}
					message := "中转端点须为公网字面量 IP，且不能指向本机"
					if endpointErr != nil {
						message = endpointErr.Error()
					}
					v.check("forward_upstream", endpointErr == nil, message)
				}
			}
		}
		if in.Enabled {
			if _, err := networkconfig.HostAddress(candidate.Config.TargetHost); err != nil {
				v.check("forward_dns", v.ForwardDNSVersion == 1, "agent 需要支持固定目标的受限 DNS 解析与过期阻断")
			}
			targetErr := netinventory.ValidateForwardSelection(candidate.Config, direct, state.inventory)
			if transport != nil {
				if _, literalErr := networkconfig.HostAddress(candidate.Config.TargetHost); literalErr != nil {
					targetErr = errors.New("中转出口的固定目标目前须使用字面量 IP")
				}
			}
			if ip, ipErr := networkconfig.HostAddress(candidate.Config.TargetHost); ipErr == nil && networkconfig.NeedsTransportGrant(ip) {
				v.check("forward_private", v.ForwardPrivateVersion == 1, "agent 需要支持固定转发的独立本机授权")
				_, targetErr = netinventory.ResolveForwardLiteral(agentproto.ForwardSpec{ForwardID: f.ID, Config: candidate.Config, Direct: direct, ForwardGrants: state.forwardGrants}, state.inventory)
			}
			message := "目标使用固定 TCP 地址；私网需要本机授权，域名按所选出口解析并验证"
			if targetErr != nil {
				message = targetErr.Error()
			}
			v.check("forward_target", targetErr == nil, message)
			if ip, ipErr := networkconfig.HostAddress(candidate.Config.TargetHost); targetErr == nil && ipErr == nil {
				family := "dual"
				if transport != nil {
					family = transport.Family
				} else if direct != nil {
					family = direct.Family
				}
				familyOK := !(ip.Is6() && (state.server.IPv4Only || family == "ipv4")) && !(ip.Is4() && family == "ipv6")
				v.check("target_family", familyOK, "目标 IP 地址族与服务器或所选出口的限制冲突")
			}
			selectionErr := netinventory.ValidateBindingSelection(candidate.Config.BindingPolicy(), direct, state.inventory, state.server.IPv4Only)
			message = "监听地址、出口接口和源地址可用"
			if selectionErr != nil {
				message = selectionErr.Error()
			}
			v.check("selection", selectionErr == nil, message)
		}
		var conflicts int
		if err = q.QueryRowContext(ctx, `SELECT count(*) FROM server_listener_reservations WHERE server_id=? AND listen_port=? AND NOT (resource_kind='forward' AND resource_id=?)`, f.ServerID, candidate.Config.ListenPort, f.ID).Scan(&conflicts); err != nil {
			return *v, err
		}
		v.check("listen_port", conflicts == 0, ErrListenPortConflict.Error())
	}
	impact := NetworkImpact{ServerID: f.ServerID, ServerEnabled: state.server.Enabled, RuntimeChange: true, ReferenceCount: 1, RestartScope: "server_singbox", Nodes: []NetworkImpactNode{}}
	nodes, err := s.impactNodes(ctx, q, state.server)
	if err != nil {
		return *v, err
	}
	for _, n := range nodes {
		if n.Core != domain.CoreSingBox || n.Revoked {
			continue
		}
		n.Effect, n.RestartPossible = "restart", true
		impact.Nodes = append(impact.Nodes, n)
		impact.RestartCount++
	}
	forwards, err := s.impactForwards(ctx, q, f.ServerID)
	if err != nil {
		return *v, err
	}
	addForwardRestarts(&impact, forwards)
	for i := range impact.Forwards {
		if impact.Forwards[i].ForwardID == f.ID {
			impact.Forwards[i].Effect = "binding"
			if in.Action == "delete" || !in.Enabled {
				impact.Forwards[i].Effect = "disable"
			}
		}
	}
	if err = s.addImpactHistory(ctx, q, &impact); err != nil {
		return *v, err
	}
	in.ID, in.ExpectedImpact = "", ""
	impact.Token, err = impactToken(impact, in)
	v.Impact = &impact
	return *v, err
}
