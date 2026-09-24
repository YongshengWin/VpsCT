package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/corecompat"
	"ctlvps/internal/domain"
	"ctlvps/internal/netinventory"
	"ctlvps/internal/networkconfig"
)

const NetworkReadinessMaxAge = 2 * time.Minute

var ErrNetworkNotReady = errors.New("服务器尚未满足网络配置条件，请刷新诊断并检查失败项目")

type NetworkReadinessCheck struct {
	Code    string `json:"code"`
	Ready   bool   `json:"ready"`
	Message string `json:"message"`
}

// Readiness describes admission from the controller's current observation. It
// never replaces the agent's fresh local checks, guard leases or apply receipt.
type NetworkReadiness struct {
	Impact                  *NetworkImpact          `json:"impact,omitempty"`
	ServerID                int64                   `json:"server_id"`
	Architecture            string                  `json:"architecture"`
	Ready                   bool                    `json:"ready"`
	BindingVersion          int                     `json:"binding_version"`
	SSHVersion              int                     `json:"ssh_version,omitempty"`
	WireGuardVersion        int                     `json:"wireguard_version,omitempty"`
	EgressVersion           int                     `json:"egress_version,omitempty"`
	ForwardDNSVersion       int                     `json:"forward_dns_version,omitempty"`
	ForwardPrivateVersion   int                     `json:"forward_private_version,omitempty"`
	ForwardTransportVersion int                     `json:"forward_transport_version,omitempty"`
	ForwardVersion          int                     `json:"forward_version,omitempty"`
	TransportVersion        int                     `json:"transport_version,omitempty"`
	PinnedCoreVersion       string                  `json:"pinned_core_version"`
	InstalledCoreVersion    string                  `json:"installed_core_version"`
	SupportedProtocols      []string                `json:"supported_protocols"`
	InventoryReceivedAt     *time.Time              `json:"inventory_received_at"`
	RequiresLocalValidation bool                    `json:"requires_local_validation"`
	Checks                  []NetworkReadinessCheck `json:"checks"`
}

func (r *NetworkReadiness) check(code string, ready bool, message string) {
	r.Checks = append(r.Checks, NetworkReadinessCheck{Code: code, Ready: ready, Message: message})
	r.Ready = r.Ready && ready
}

type networkReadinessState struct {
	view          NetworkReadiness
	server        domain.Server
	inventory     *agentproto.NetworkSnapshot
	grants        []networkconfig.TransportGrant
	forwardGrants []networkconfig.ForwardGrant
}

func (s *Store) networkReadiness(ctx context.Context, q querier, serverID int64) (networkReadinessState, error) {
	return s.networkReadinessForCore(ctx, q, serverID, domain.CoreSingBox)
}
func (s *Store) networkReadinessForCore(ctx context.Context, q querier, serverID int64, coreKind domain.Core) (networkReadinessState, error) {
	state := networkReadinessState{view: NetworkReadiness{ServerID: serverID, Ready: true, BindingVersion: agentproto.NetworkBindingVersion,
		SupportedProtocols: agentproto.DirectBindingProtocols(), RequiresLocalValidation: true, Checks: []NetworkReadinessCheck{}}}
	v := &state.view
	setting, fallback := bindingCoreSetting(coreKind)
	if setting == "" {
		return state, ErrNetworkCoreVersion
	}
	if coreKind != domain.CoreSingBox {
		v.SupportedProtocols = []string{string(coreKind)}
	}
	var err error
	state.server, err = scanServer(q.QueryRowContext(ctx, `SELECT `+serverCols+` FROM servers WHERE id=?`, serverID))
	if isNoRows(err) {
		return state, ErrNotFound
	}
	if err != nil {
		return state, err
	}
	a, err := scanAgent(q.QueryRowContext(ctx, `SELECT `+agentCols+` FROM agents WHERE server_id=?`, serverID))
	if err != nil && !isNoRows(err) {
		return state, err
	}
	now := s.Now()
	fresh := func(at *time.Time) bool {
		return at != nil && !at.IsZero() && now.Sub(*at) >= -time.Second && now.Sub(*at) <= NetworkReadinessMaxAge
	}
	v.check("agent_online", a.TokenHash != "" && fresh(a.LastSeenAt), "需要最近两分钟内上报的已注册 agent")
	var diag agentproto.Diagnostics
	diagnosticsValid := json.Unmarshal(a.Diagnostics, &diag) == nil && agentproto.ValidateNetworkDiagnostics(diag) == nil
	if diagnosticsValid {
		v.EgressVersion = diag.NetworkEgressVersion
		v.SSHVersion = diag.NetworkSSHVersion
		v.WireGuardVersion = diag.NetworkWireGuardVersion
		v.ForwardVersion = diag.NetworkForwardVersion
		v.ForwardDNSVersion = diag.ForwardDNSVersion
		v.ForwardPrivateVersion = diag.ForwardPrivateVersion
		v.ForwardTransportVersion = diag.ForwardTransportVersion
		state.forwardGrants = diag.ForwardGrants
		v.TransportVersion = diag.NetworkTransportVersion
		state.grants = diag.TransportGrants
	}
	var metrics agentproto.Metrics
	if json.Unmarshal(a.Metrics, &metrics) == nil {
		v.Architecture = metrics.Arch
	}
	// Match the distributed Linux binaries and sandbox ABIs. Host capabilities
	// and fresh inventory below remain mandatory on both architectures.
	v.check("architecture", v.Architecture == "arm64" || v.Architecture == "amd64", "网络绑定需要 Linux AMD64 或 ARM64 agent")
	if coreKind != domain.CoreSingBox {
		v.check("listen_protocol", diagnosticsValid && diag.ListenBindingVersion == 1, "agent 需要支持 Snell/mieru 独立监听保护")
	}
	v.check("binding_protocol", diagnosticsValid && diag.NetworkBindingVersion == agentproto.NetworkBindingVersion, "agent 必须支持当前网络配置协议")
	var requiredEgress int
	err = q.QueryRowContext(ctx, "SELECT egress_version FROM server_network_requirements WHERE server_id=?", serverID).Scan(&requiredEgress)
	if err != nil && !isNoRows(err) {
		return state, err
	}
	if requiredEgress > 0 {
		v.check("egress_protocol", diagnosticsValid && diag.NetworkEgressVersion == agentproto.NetworkEgressVersion, "曾启用中转的服务器需要兼容 agent 执行变更和清理")
	}
	var requiredWG int
	err = q.QueryRowContext(ctx, "SELECT wireguard_version FROM server_network_requirements WHERE server_id=?", serverID).Scan(&requiredWG)
	if err != nil && !isNoRows(err) {
		return state, err
	}
	if requiredWG > 0 {
		v.check("wireguard_protocol", diagnosticsValid && diag.NetworkWireGuardVersion == agentproto.NetworkWireGuardVersion, "WireGuard 中转或清理需要兼容的 agent")
	}
	var requiredSSH int
	err = q.QueryRowContext(ctx, "SELECT ssh_version FROM server_network_requirements WHERE server_id=?", serverID).Scan(&requiredSSH)
	if err != nil && !isNoRows(err) {
		return state, err
	}
	if requiredSSH > 0 {
		v.check("ssh_protocol", diagnosticsValid && diag.NetworkSSHVersion == agentproto.NetworkSSHVersion, "SSH 中转及其清理需要兼容的 agent")
	}
	var requiredForward int
	err = q.QueryRowContext(ctx, "SELECT forward_version FROM server_network_requirements WHERE server_id=?", serverID).Scan(&requiredForward)
	if err != nil && !isNoRows(err) {
		return state, err
	}
	if requiredForward > 0 {
		v.check("forward_protocol", diagnosticsValid && diag.NetworkForwardVersion == agentproto.NetworkForwardVersion, "存在固定转发或清理任务，需要兼容 agent 执行网络变更")
	}
	v.check("local_policy", diagnosticsValid && diag.SecurityVersion >= 1 && diag.SecurityPolicy && !diag.SecurityPaused && diag.NetworkConfigureAllowed, "本机安全策略必须允许 agent 配置且未暂停变更")
	v.check("systemd", diagnosticsValid && diag.Systemd, "需要 systemd 管理代理进程")
	v.check("nftables", diagnosticsValid && diag.Nftables, "需要 nftables 执行计量和出口保护")
	v.check("metering", diagnosticsValid && diag.NetworkBindingVersion == agentproto.NetworkBindingVersion && diag.MeteringError == "", "节点计量必须可用")
	v.check("network_guard", diagnosticsValid && diag.NetworkBindingVersion == agentproto.NetworkBindingVersion && diag.NetworkGuardError == "", "本机网络保护任务必须正常")
	var active int
	if err = q.QueryRowContext(ctx, `SELECT count(*) FROM maintenance_jobs WHERE server_id=? AND status IN ('queued','running')`, serverID).Scan(&active); err != nil {
		return state, err
	}
	v.check("maintenance", active == 0, "等待服务器维护任务结束后才能修改网络")
	err = q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, setting).Scan(s.scanSecret("settings.value", &v.PinnedCoreVersion))
	if err != nil && !isNoRows(err) {
		return state, err
	}
	v.PinnedCoreVersion = strings.TrimPrefix(strings.TrimSpace(v.PinnedCoreVersion), "v")
	if v.PinnedCoreVersion == "" {
		v.PinnedCoreVersion = fallback
	}
	v.check("pinned_core", agentproto.NetworkBindingSupported(string(coreKind), v.PinnedCoreVersion), "所选内核版本必须支持网络绑定")
	var installed, seenCore bool
	for _, core := range diag.Cores {
		if core.Name == agentproto.CoreVersionKey(string(coreKind)) {
			// Duplicate contradictory entries must not select an arbitrary
			// favorable result from a malformed diagnostic payload.
			if seenCore {
				installed = false
				break
			}
			seenCore = true
			v.InstalledCoreVersion = strings.TrimPrefix(strings.TrimSpace(core.Version), "v")
			installed = core.Installed && v.InstalledCoreVersion != "" && v.InstalledCoreVersion == v.PinnedCoreVersion
		}
	}
	v.check("installed_core", installed, "已安装内核必须与锁定的支持版本一致；不会因选择网络功能自动升级")
	var raw, received string
	err = q.QueryRowContext(ctx, `SELECT snapshot,received_at FROM network_snapshots WHERE server_id=?`, serverID).Scan(&raw, &received)
	if err != nil && !isNoRows(err) {
		return state, err
	}
	if err == nil {
		at := parseTime(received)
		v.InventoryReceivedAt = &at
		if json.Unmarshal([]byte(raw), &state.inventory) != nil {
			state.inventory = nil
		}
	}
	complete := state.inventory != nil && state.inventory.Status == "ok" && agentproto.ValidateNetwork(state.inventory) == nil
	v.check("inventory", complete && fresh(v.InventoryReceivedAt), "需要最近两分钟内收到的完整有效网卡清单")
	return state, nil
}

// NetworkCapabilities reads one SQLite snapshot of admission prerequisites.
func (s *Store) NetworkCapabilities(ctx context.Context, serverID int64) (NetworkReadiness, error) {
	return s.NetworkCapabilitiesForCore(ctx, serverID, domain.CoreSingBox)
}
func (s *Store) NetworkCapabilitiesForCore(ctx context.Context, serverID int64, coreKind domain.Core) (NetworkReadiness, error) {
	var state networkReadinessState
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		state, err = s.networkReadinessForCore(ctx, tx, serverID, coreKind)
		return err
	})
	return state.view, err
}

func (s *Store) previewNodeNetwork(ctx context.Context, q querier, n domain.Node, policy *networkconfig.Node) (NetworkReadiness, error) {
	return s.previewNodeNetworkForEgress(ctx, q, n, policy, 0)
}

func (s *Store) previewNodeNetworkForEgress(ctx context.Context, q querier, n domain.Node, policy *networkconfig.Node, enablingProfileID int64) (NetworkReadiness, error) {
	if n.Source != domain.NodeDeployed || n.ServerID == nil {
		return NetworkReadiness{}, errors.New("只有受管部署节点可以设置服务器网络")
	}
	state, err := s.networkReadinessForCore(ctx, q, *n.ServerID, n.Core)
	if err != nil {
		return state.view, err
	}
	v := &state.view
	if policy == nil {
		// Reset still needs a compatible online agent to remove local fences.
		// It need not resolve an interface which has disappeared or use the
		// currently selected core as a newly admitted binding.
		v.Ready = true
		checks := make([]NetworkReadinessCheck, 0, 4)
		for _, c := range v.Checks {
			switch c.Code {
			case "agent_online", "binding_protocol", "listen_protocol", "egress_protocol", "ssh_protocol", "wireguard_protocol", "forward_protocol", "local_policy", "maintenance":
				checks = append(checks, c)
				v.Ready = v.Ready && c.Ready
			}
		}
		v.Checks = checks
		return *v, nil
	}
	var params map[string]any
	_ = json.Unmarshal(n.ServerParams, &params)
	spec := agentproto.NodeSpec{Core: string(n.Core), Protocol: n.Protocol, Params: params}
	qualified := agentproto.DirectBindingSupported(spec, v.PinnedCoreVersion) || (agentproto.ListenBindingSupported(spec, v.PinnedCoreVersion) && policy.EgressProfileID == 0)
	v.check("node_protocol", qualified, "直连绑定支持 SS-2022 AES-128、VLESS Reality、Trojan、AnyTLS、Hysteria2 和 TUIC；Snell/mieru 只支持指定监听")
	var direct *networkconfig.Direct
	if policy.EgressProfileID != 0 {
		p, err := scanEgress(q.QueryRowContext(ctx, `SELECT `+egressCols+` FROM egress_profiles WHERE id=? AND server_id=?`, policy.EgressProfileID, *n.ServerID))
		if err != nil {
			return *v, err
		}
		v.check("egress_enabled", p.Enabled || p.ID == enablingProfileID, "不能绑定已停用的出口配置")
		var raw string
		err = q.QueryRowContext(ctx, `SELECT config FROM egress_profile_revisions WHERE profile_id=? AND revision=? AND server_id=?`, p.ID, policy.EgressRevision, *n.ServerID).Scan(&raw)
		if isNoRows(err) {
			return *v, ErrNotFound
		}
		if err != nil {
			return *v, err
		}
		switch p.Kind {
		case "direct":
			cfg, err := networkconfig.DecodeDirect([]byte(raw))
			if err != nil {
				return *v, err
			}
			direct = &cfg
		case "socks5", "ssh", "wireguard", "ss2022":
			v.check("transport_inbound", agentproto.SOCKS5BindingSupported(spec, v.PinnedCoreVersion), "经中转出口的入口目前需要 SS-2022 AES-128")
			if p.Kind == "ss2022" {
				v.check("ss2022_core", corecompat.SS2022Outbound(v.PinnedCoreVersion), corecompat.SS2022Requirement)
			}
			var cfg networkconfig.SOCKS5
			var err error
			if p.Kind == "wireguard" {
				var wg networkconfig.WireGuard
				wg, err = networkconfig.DecodeWireGuard([]byte(raw))
				cfg = wg.Transport()
				v.check("wireguard_protocol", v.WireGuardVersion == agentproto.NetworkWireGuardVersion, "WireGuard 出口需要兼容 agent")
				identityErr := s.claimWireGuardIdentity(ctx, q, *n.ServerID, n.ID, p.ID, policy.EgressRevision, false)
				message := "WireGuard 私钥只能由原节点使用"
				if identityErr != nil {
					message = identityErr.Error()
				}
				v.check("wireguard_identity", identityErr == nil, message)
			} else if p.Kind == "ssh" {
				var ssh networkconfig.SSH
				ssh, err = networkconfig.DecodeSSH([]byte(raw))
				cfg = ssh.Transport()
				v.check("ssh_protocol", v.SSHVersion == agentproto.NetworkSSHVersion, "SSH 出口需要支持主机公钥校验和独立协议的 agent")
			} else if p.Kind == "ss2022" {
				var ss networkconfig.SS2022
				ss, err = networkconfig.DecodeSS2022([]byte(raw))
				cfg = ss.Transport()
			} else {
				cfg, err = networkconfig.DecodeSOCKS5([]byte(raw))
			}
			if err != nil {
				return *v, err
			}
			v.check("socks5_protocol", v.EgressVersion == agentproto.NetworkEgressVersion, "SOCKS5 出口需要支持中转配置、端点保护和本地刷新版本的 agent")
			effective, err := networkconfig.EffectiveSOCKS5(cfg, state.server.IPv4Only)
			if err != nil {
				return *v, err
			}
			direct = &effective.Outer
			var endpointErr error
			if ip, err := netip.ParseAddr(effective.Server); err == nil {
				pin := networkconfig.ResolvedSOCKS5{Purpose: effective.Purpose, Address: ip.String(), Port: effective.ServerPort, UDP: effective.UDP}
				pin, endpointErr = pin.WithTransportGrants(n.ID, policy.EgressProfileID, state.grants)
				if endpointErr == nil {
					endpointErr = netinventory.ValidateSOCKS5Endpoint(pin, state.inventory)
				}
			} else {
				endpointErr = netinventory.ValidateBootstrapEndpoint(n.ID, policy.EgressProfileID, effective.Outer.DNS, state.inventory, state.grants)
			}
			message := "私网上游和启动 DNS 需本机授权；agent 在应用时重新检查授权、实际解析结果及本机地址"
			if endpointErr != nil {
				message = endpointErr.Error()
			}
			v.check("socks5_endpoint", endpointErr == nil, message)
		default:
			v.check("egress_kind", false, "该出口类型尚未通过运行验证")
		}
	}
	selectionErr := netinventory.ValidateBindingSelection(*policy, direct, state.inventory, state.server.IPv4Only)
	message := "所选监听地址、出口接口和源地址必须在清单中有效且属于原接口"
	if selectionErr != nil {
		message = selectionErr.Error() // structured selection errors contain no secrets
	}
	v.check("selection", selectionErr == nil, message)
	return *v, nil
}

func (s *Store) PreviewNodeNetwork(ctx context.Context, nodeID int64, policy *networkconfig.Node) (NetworkReadiness, error) {
	var view NetworkReadiness
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		n, err := s.scanNode(tx.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE id=?`, nodeID))
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		view, err = s.previewNodeNetwork(ctx, tx, n, policy)
		return err
	})
	return view, err
}
