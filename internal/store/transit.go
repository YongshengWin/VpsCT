package store

import (
	"context"
	"crypto/sha256"
	"ctlvps/internal/agentproto"
	"ctlvps/internal/corecompat"
	"ctlvps/internal/domain"
	"ctlvps/internal/netinventory"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/provision"
	"ctlvps/internal/wgconfig"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

var ErrManagedTransit = errors.New("该资源由托管中转维护，请先通过托管中转页面退役")
var ErrTransitRequest = errors.New("托管中转请求无效")

type TransitRequest struct {
	Protocol         string                 `json:"protocol,omitempty"`
	Family           string                 `json:"family"`
	ID               string                 `json:"operation_id"`
	EntryNodeID      int64                  `json:"entry_node_id"`
	ExpectedRevision int64                  `json:"expected_revision"`
	LandingServerID  int64                  `json:"landing_server_id"`
	LandingAddress   string                 `json:"landing_address"`
	LandingPort      int                    `json:"landing_port"`
	Outer            networkconfig.Direct   `json:"outer"`
	DNS              networkconfig.Resolver `json:"dns"`
	ExpectedImpact   string                 `json:"expected_impact,omitempty"`
}
type ManagedTransit struct {
	Protocol        string                  `json:"protocol"`
	ID              string                  `json:"id"`
	RequestHash     string                  `json:"-"`
	EntryServerID   int64                   `json:"entry_server_id"`
	LandingServerID int64                   `json:"landing_server_id"`
	EntryNodeID     int64                   `json:"entry_node_id"`
	LandingNodeID   int64                   `json:"landing_node_id"`
	ProfileID       int64                   `json:"profile_id"`
	Stage           string                  `json:"stage"`
	Hidden          bool                    `json:"hidden"`
	CreatedAt       string                  `json:"created_at"`
	UpdatedAt       string                  `json:"updated_at"`
	EntryChecks     []NetworkReadinessCheck `json:"entry_checks,omitempty"`
	EntryStatus     string                  `json:"entry_status,omitempty"`
	LandingStatus   string                  `json:"landing_status,omitempty"`
}

const transitCols = `id,request_hash,entry_server_id,landing_server_id,entry_node_id,landing_node_id,profile_id,stage,created_at,updated_at,COALESCE((SELECT kind FROM egress_profiles WHERE id=profile_id),'wireguard'),hidden`

func scanTransit(row interface{ Scan(...any) error }) (ManagedTransit, error) {
	var t ManagedTransit
	err := row.Scan(&t.ID, &t.RequestHash, &t.EntryServerID, &t.LandingServerID, &t.EntryNodeID, &t.LandingNodeID, &t.ProfileID, &t.Stage, &t.CreatedAt, &t.UpdatedAt, &t.Protocol, &t.Hidden)
	if isNoRows(err) {
		err = ErrNotFound
	}
	return t, err
}
func (s *Store) ManagedTransits(ctx context.Context, serverID int64) ([]ManagedTransit, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+transitCols+` FROM managed_transits WHERE ?=0 OR entry_server_id=? OR landing_server_id=? ORDER BY created_at DESC LIMIT 256`, serverID, serverID, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ManagedTransit{}
	for rows.Next() {
		v, e := scanTransit(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].EntryStatus = s.transitServerStatus(ctx, out[i].EntryServerID)
		out[i].LandingStatus = s.transitServerStatus(ctx, out[i].LandingServerID)
		if out[i].Stage == "pending_landing" && out[i].LandingStatus == "applied" {
			_ = s.Tx(ctx, func(tx *sql.Tx) error {
				view, e := s.transitEntryReadiness(ctx, tx, out[i])
				if e != nil {
					return e
				}
				for _, check := range view.Checks {
					if !check.Ready {
						out[i].EntryChecks = append(out[i].EntryChecks, check)
					}
				}
				return nil
			})
		}
	}
	return out, nil
}
func (s *Store) ManagedTransit(ctx context.Context, id string) (ManagedTransit, error) {
	return scanTransit(s.db.QueryRowContext(ctx, `SELECT `+transitCols+` FROM managed_transits WHERE id=?`, id))
}

// SetManagedTransitHidden only changes whether a completed transit appears in
// the panel. Its ledger row must remain for retained meter identity and audit.
func (s *Store) SetManagedTransitHidden(ctx context.Context, id, expectedUpdatedAt string, hidden bool) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		current, err := scanTransit(tx.QueryRowContext(ctx, `SELECT `+transitCols+` FROM managed_transits WHERE id=?`, id))
		if err != nil {
			return err
		}
		if current.Stage != "retired" {
			return ErrNetworkConflict
		}
		if current.Hidden == hidden {
			return nil
		}
		if current.UpdatedAt != expectedUpdatedAt {
			return ErrNetworkConflict
		}
		result, err := tx.ExecContext(ctx, `UPDATE managed_transits SET hidden=?,updated_at=? WHERE id=? AND stage='retired' AND updated_at=?`, hidden, fmtTime(s.Now()), id, expectedUpdatedAt)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return ErrNetworkConflict
		}
		return nil
	})
}

type TransitPreview struct {
	Ready   bool                    `json:"ready"`
	Token   string                  `json:"token"`
	Entry   NetworkImpact           `json:"entry"`
	Landing NetworkImpact           `json:"landing"`
	Checks  []NetworkReadinessCheck `json:"checks"`
}

func transitProtocol(raw string) (string, error) {
	if raw == "" || raw == "wireguard" {
		return "wireguard", nil
	}
	if raw == "ss2022" {
		return raw, nil
	}
	return "", fmt.Errorf("%w: 不支持的中转协议", ErrTransitRequest)
}

func (s *Store) previewTransit(ctx context.Context, q querier, in TransitRequest) (TransitPreview, domain.Node, domain.Server, error) {
	var v TransitPreview
	var landing domain.Server
	protocol, protocolErr := transitProtocol(in.Protocol)
	if protocolErr != nil {
		return v, domain.Node{}, landing, protocolErr
	}
	n, err := s.scanNode(q.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE id=?`, in.EntryNodeID))
	if err != nil {
		return v, n, landing, err
	}
	if n.Source != domain.NodeDeployed || n.ServerID == nil || *n.ServerID == in.LandingServerID || n.NetworkRevision != in.ExpectedRevision || n.Protocol != domain.ProtocolShadowsocks || n.Revoked {
		return v, n, landing, fmt.Errorf("%w: 须选择不同服务器上的有效 SS-2022 入口，并核对网络版本", ErrTransitRequest)
	}
	var p struct {
		Method string `json:"method"`
	}
	_ = json.Unmarshal(n.ServerParams, &p)
	if p.Method != "2022-blake3-aes-128-gcm" {
		return v, n, landing, fmt.Errorf("%w: 当前托管中转入口要求 SS-2022 AES-128", ErrTransitRequest)
	}
	if in.LandingPort < 1025 || in.LandingPort > 65535 {
		return v, n, landing, fmt.Errorf("%w: 落地端口须为 1025–65535", ErrTransitRequest)
	}
	if in.Family != "ipv4" && in.Family != "ipv6" && in.Family != "dual" {
		return v, n, landing, fmt.Errorf("%w: 业务地址族无效", ErrTransitRequest)
	}
	if err = in.Outer.Validate(); err != nil {
		return v, n, landing, fmt.Errorf("%w: %s", ErrTransitRequest, err)
	}
	if err = networkconfig.ValidateAdvertiseHost(in.LandingAddress); err != nil {
		return v, n, landing, fmt.Errorf("%w: 落地地址无效", ErrTransitRequest)
	}
	if ip, e := netip.ParseAddr(in.LandingAddress); e == nil && !networkconfig.NeedsTransportGrant(ip) {
		if e = (networkconfig.ResolvedSOCKS5{Purpose: protocol, Address: ip.String(), Port: in.LandingPort, UDP: true}).Validate(); e != nil {
			return v, n, landing, fmt.Errorf("%w: 落地 IP 不符合端点策略", ErrTransitRequest)
		}
	}
	entryState, err := s.networkReadiness(ctx, q, *n.ServerID)
	if err != nil {
		return v, n, landing, err
	}
	landState, err := s.networkReadiness(ctx, q, in.LandingServerID)
	if err != nil {
		return v, n, landing, err
	}
	landing = landState.server
	if landing.IPv4Only && in.Family != "ipv4" {
		return v, n, landing, fmt.Errorf("%w: 落地服务器仅允许 IPv4，请选择 IPv4 中转", ErrTransitRequest)
	}
	var ag4, ag6 string
	if err = q.QueryRowContext(ctx, `SELECT public_ipv4,public_ipv6 FROM agents WHERE server_id=?`, landing.ID).Scan(&ag4, &ag6); err != nil {
		return v, n, landing, err
	}
	owned := strings.EqualFold(in.LandingAddress, landing.PublicHost) || in.LandingAddress == ag4 || in.LandingAddress == ag6
	if ip, e := netip.ParseAddr(in.LandingAddress); e == nil && landState.inventory != nil {
		for _, nic := range landState.inventory.Interfaces {
			for _, address := range nic.Addresses {
				prefix, e := netip.ParsePrefix(address)
				if (e == nil && prefix.Addr() == ip) || address == ip.String() {
					owned = true
				}
			}
		}
	}
	if !owned {
		return v, n, landing, fmt.Errorf("%w: 落地地址须匹配配置的访问地址或 agent 报告的 IP", ErrTransitRequest)
	}
	v.Ready = entryState.view.Ready && landState.view.Ready && landing.Enabled && entryState.server.Enabled
	v.Checks = append(entryState.view.Checks, landState.view.Checks...)
	var landingDiagRaw string
	if err = q.QueryRowContext(ctx, `SELECT diagnostics FROM agents WHERE server_id=?`, landing.ID).Scan(&landingDiagRaw); err != nil {
		return v, n, landing, err
	}
	var landingDiag agentproto.Diagnostics
	_ = json.Unmarshal([]byte(landingDiagRaw), &landingDiag)
	protocolReady := entryState.view.WireGuardVersion == 1 && landState.view.WireGuardVersion == 1 && landingDiag.MeterInventoryVersion == 1
	message := "两端需支持 WireGuard，落地 agent 还需支持最终计量清理回执"
	if protocol == "ss2022" {
		protocolReady = corecompat.SS2022Outbound(entryState.view.PinnedCoreVersion) && entryState.view.EgressVersion == agentproto.NetworkEgressVersion && landState.view.BindingVersion == agentproto.NetworkBindingVersion && landingDiag.MeterInventoryVersion == 1
		message = "SS-2022 入口需官方 sing-box 1.14.1 或兼容 1.14.x，两端 agent 需支持网络绑定与计量清理"
	}
	v.Checks = append(v.Checks, NetworkReadinessCheck{Code: "transit_protocol", Ready: protocolReady, Message: message})
	v.Ready = v.Ready && protocolReady
	if err = netinventory.ValidateBindingSelection(networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block", EgressProfileID: 1, EgressRevision: 1}, &in.Outer, entryState.inventory, entryState.server.IPv4Only); err != nil {
		return v, n, landing, err
	}
	// Validate business DNS using the same transport schema as external peers.
	var transport networkconfig.SOCKS5
	if protocol == "wireguard" {
		transport = (networkconfig.WireGuard{Server: in.LandingAddress, ServerPort: in.LandingPort, Family: in.Family, DNS: in.DNS, Outer: in.Outer, ConnectTimeoutSeconds: 10}).Transport()
	} else {
		transport = (networkconfig.SS2022{Server: in.LandingAddress, ServerPort: in.LandingPort, Method: "2022-blake3-aes-128-gcm", Family: in.Family, DNS: in.DNS, Outer: in.Outer, ConnectTimeoutSeconds: 10}).Transport()
	}
	if err = transport.Validate(); err != nil {
		return v, n, landing, err
	}
	var reserved bool
	if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM server_listener_reservations WHERE server_id=? AND listen_port=?)`, landing.ID, in.LandingPort).Scan(&reserved); err != nil {
		return v, n, landing, err
	}
	if reserved {
		return v, n, landing, fmt.Errorf("%w: 落地端口已被占用", ErrTransitRequest)
	}
	if err = s.transitImpacts(ctx, q, &v, entryState.server, landing, in.EntryNodeID); err != nil {
		return v, n, landing, err
	}
	candidate := in
	candidate.ID = ""
	candidate.ExpectedImpact = ""
	raw, _ := json.Marshal(struct {
		Request        TransitRequest
		Entry, Landing NetworkImpact
	}{candidate, v.Entry, v.Landing})
	sum := sha256.Sum256(raw)
	v.Token = hex.EncodeToString(sum[:])
	return v, n, landing, nil
}
func (s *Store) transitImpacts(ctx context.Context, q querier, v *TransitPreview, entry, landing domain.Server, entryNodeID int64) error {
	for _, item := range []struct {
		srv domain.Server
		out *NetworkImpact
	}{{entry, &v.Entry}, {landing, &v.Landing}} {
		*item.out = NetworkImpact{ServerID: item.srv.ID, ServerEnabled: item.srv.Enabled, RuntimeChange: true, RestartScope: "server_singbox"}
		var err error
		item.out.Nodes, err = s.impactNodes(ctx, q, item.srv)
		if err != nil {
			return err
		}

		for i := range item.out.Nodes {
			node := &item.out.Nodes[i]
			node.Effect = "restart"
			node.RestartPossible = node.Core == domain.CoreSingBox && !node.Revoked
			if node.RestartPossible {
				item.out.RestartCount++
			}
			if node.NodeID == entryNodeID {
				node.Effect = "binding"
				item.out.ReferenceCount++
			}
		}
		if err = s.addNodeForwardImpact(ctx, q, item.out, domain.CoreSingBox); err != nil {
			return err
		}
		if err = s.addImpactHistory(ctx, q, item.out); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) transitRetirementPreview(ctx context.Context, q querier, t ManagedTransit) (TransitPreview, error) {
	v := TransitPreview{Ready: true}
	entry, e := scanServer(q.QueryRowContext(ctx, `SELECT `+serverCols+` FROM servers WHERE id=?`, t.EntryServerID))
	if e != nil {
		return v, e
	}
	landing, e := scanServer(q.QueryRowContext(ctx, `SELECT `+serverCols+` FROM servers WHERE id=?`, t.LandingServerID))
	if e != nil {
		return v, e
	}
	if e = s.transitImpacts(ctx, q, &v, entry, landing, t.EntryNodeID); e != nil {
		return v, e
	}
	raw, _ := json.Marshal(struct {
		ID, Stage, UpdatedAt string
		Entry, Landing       NetworkImpact
	}{t.ID, t.Stage, t.UpdatedAt, v.Entry, v.Landing})
	sum := sha256.Sum256(raw)
	v.Token = hex.EncodeToString(sum[:])
	return v, nil
}
func (s *Store) PreviewTransitRetirement(ctx context.Context, id string) (TransitPreview, error) {
	var v TransitPreview
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		t, e := scanTransit(tx.QueryRowContext(ctx, `SELECT `+transitCols+` FROM managed_transits WHERE id=?`, id))
		if e != nil {
			return e
		}
		v, e = s.transitRetirementPreview(ctx, tx, t)
		return e
	})
	return v, err
}

func (s *Store) PreviewManagedTransit(ctx context.Context, in TransitRequest) (TransitPreview, error) {
	var v TransitPreview
	err := s.Tx(ctx, func(tx *sql.Tx) error { var err error; v, _, _, err = s.previewTransit(ctx, tx, in); return err })
	return v, err
}
func (s *Store) CreateManagedTransit(ctx context.Context, in TransitRequest, audit domain.AuditEvent) (ManagedTransit, error) {
	var out ManagedTransit
	protocol, protocolErr := transitProtocol(in.Protocol)
	if protocolErr != nil {
		return out, protocolErr
	}
	if !networkconfig.ValidIdentity(in.ID) {
		return out, fmt.Errorf("%w: 操作编号无效", ErrTransitRequest)
	}
	raw, _ := json.Marshal(in)
	digest := sha256.Sum256(raw)
	hash := hex.EncodeToString(digest[:])
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		existing, e := scanTransit(tx.QueryRowContext(ctx, `SELECT `+transitCols+` FROM managed_transits WHERE id=?`, in.ID))
		if e == nil {
			if existing.RequestHash != hash {
				return ErrNetworkOperationConflict
			}
			out = existing
			return nil
		}
		if !errors.Is(e, ErrNotFound) {
			return e
		}
		v, n, landing, e := s.previewTransit(ctx, tx, in)
		if e != nil {
			return e
		}
		if !v.Ready {
			return ErrNetworkNotReady
		}
		if in.ExpectedImpact == "" {
			return ErrNetworkImpactRequired
		}
		if in.ExpectedImpact != v.Token {
			return ErrNetworkImpactChanged
		}
		var count int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM managed_transits`).Scan(&count); e != nil {
			return e
		}
		if count >= 256 {
			return fmt.Errorf("%w: 托管中转记录达到上限", ErrTransitRequest)
		}
		var endpoint domain.Node
		var profileConfig json.RawMessage
		var secret networkconfig.SOCKS5Credentials
		if protocol == "wireguard" {
			c, server, e := wgconfig.Generate(landing.IPv4Only || in.Family == "ipv4")
			if e != nil {
				return e
			}
			if in.Family == "ipv6" {
				c.IP = ""
				c.AllowedIPs = []string{"::/0"}
				server.Address = server.Address[1:]
				server.PeerAddress = server.PeerAddress[1:]
			}
			serverJSON, _ := json.Marshal(server)
			endpoint = domain.Node{Name: "托管 WireGuard " + in.ID[:8], Protocol: domain.ProtocolWireGuard, Server: in.LandingAddress, Port: in.LandingPort, ListenPort: in.LandingPort, Source: domain.NodeTransit, ServerID: &landing.ID, Core: domain.CoreSingBox, Enabled: true, Params: json.RawMessage(`{}`), ServerParams: serverJSON}
			cfg := networkconfig.WireGuard{Server: in.LandingAddress, ServerPort: in.LandingPort, PublicKey: c.PublicKey, Addresses: c.Addresses(), AllowedIPs: c.AllowedIPs, MTU: c.MTU, PersistentKeepalive: 25, Family: in.Family, DNS: in.DNS, Outer: in.Outer, ConnectTimeoutSeconds: 10}
			profileConfig, _ = json.Marshal(cfg)
			secret = networkconfig.SOCKS5Credentials{WireGuardPrivateKey: c.PrivateKey, WireGuardPresharedKey: c.PresharedKey}
		} else {
			key := provision.SS2022Key(16)
			serverJSON, _ := json.Marshal(map[string]any{"method": "2022-blake3-aes-128-gcm", "password": key})
			endpoint = domain.Node{Name: "托管 SS-2022 " + in.ID[:8], Protocol: domain.ProtocolShadowsocks, Server: in.LandingAddress, Port: in.LandingPort, ListenPort: in.LandingPort, Source: domain.NodeTransit, ServerID: &landing.ID, Core: domain.CoreSingBox, Enabled: true, Params: json.RawMessage(`{}`), ServerParams: serverJSON}
			cfg := networkconfig.SS2022{Server: in.LandingAddress, ServerPort: in.LandingPort, Method: "2022-blake3-aes-128-gcm", Family: in.Family, DNS: in.DNS, Outer: in.Outer, ConnectTimeoutSeconds: 10}
			profileConfig, _ = json.Marshal(cfg)
			secret = networkconfig.SOCKS5Credentials{Password: key}
		}
		if e = s.createNode(ctx, tx, &endpoint); e != nil {
			return e
		}
		profile, e := s.createEgressProfileWithCredentials(ctx, tx, domain.EgressProfile{ServerID: *n.ServerID, Name: endpoint.Name, Kind: protocol, Enabled: true}, profileConfig, &secret)
		if e != nil {
			return e
		}
		policy := networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block"}
		if n.Network != nil {
			policy = *n.Network
		}
		policy.EgressProfileID, policy.EgressRevision = profile.ID, 1
		ready, e := s.previewNodeNetwork(ctx, tx, n, &policy)
		if e != nil {
			return e
		}
		if !ready.Ready {
			// A private peer's local grant needs the allocated profile ID. Persist
			// the disabled intent so root can grant it; no other readiness gap is deferred.
			for _, check := range ready.Checks {
				if !check.Ready && check.Code != "socks5_endpoint" {
					return ErrNetworkNotReady
				}
			}
		}
		if _, e = s.setNodeNetwork(ctx, tx, n.ID, n.NetworkRevision, &policy, nil); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE egress_profiles SET enabled=0 WHERE id=?`, profile.ID); e != nil {
			return e
		}
		now := fmtTime(s.Now())
		out = ManagedTransit{ID: in.ID, Protocol: protocol, RequestHash: hash, EntryServerID: *n.ServerID, LandingServerID: landing.ID, EntryNodeID: n.ID, LandingNodeID: endpoint.ID, ProfileID: profile.ID, Stage: "pending_landing", CreatedAt: now, UpdatedAt: now}
		_, e = tx.ExecContext(ctx, `INSERT INTO managed_transits(id,request_hash,entry_server_id,landing_server_id,entry_node_id,landing_node_id,profile_id,stage,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, out.ID, hash, out.EntryServerID, out.LandingServerID, out.EntryNodeID, out.LandingNodeID, out.ProfileID, out.Stage, now, now)
		if e != nil {
			return e
		}
		audit.Action, audit.Target = "transit.create", out.ID
		audit.Detail = json.RawMessage(`{}`)
		return s.addAudit(ctx, tx, audit)
	})
	return out, err
}

// Advance is a compare-and-swap after the worker has observed exact receipts.
// Revocation always fences the entrance before removing the landing endpoint.
func (s *Store) AdvanceManagedTransit(ctx context.Context, t ManagedTransit, next string) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		current, e := scanTransit(tx.QueryRowContext(ctx, `SELECT `+transitCols+` FROM managed_transits WHERE id=?`, t.ID))
		if e != nil {
			return e
		}
		if current.Stage != t.Stage {
			return ErrNetworkConflict
		}
		receiptServer := t.LandingServerID
		if t.Stage == "pending_entry" || t.Stage == "stopping_entry" {
			receiptServer = t.EntryServerID
		}
		if e = networkMaintenance(ctx, tx, receiptServer); e != nil {
			return e
		}
		var applied bool
		e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agents a JOIN server_network_generations g ON g.server_id=a.server_id JOIN desired_states d ON d.server_id=a.server_id AND d.revision=a.applied_revision AND d.hash=a.applied_hash WHERE a.server_id=? AND a.apply_error='' AND g.generation=g.published_generation AND d.revision=(SELECT MAX(revision) FROM desired_states WHERE server_id=a.server_id))`, receiptServer).Scan(&applied)
		if e != nil {
			return e
		}
		if !applied {
			return ErrNetworkConflict
		}
		allowed := map[string]string{"pending_landing": "pending_entry", "pending_entry": "applied", "stopping_entry": "stopping_landing", "stopping_landing": "retired"}
		if allowed[t.Stage] != next {
			return errors.New("无效的中转阶段")
		}
		if next == "pending_entry" {
			ready, err := s.transitEntryReadiness(ctx, tx, current)
			if err != nil {
				return err
			}
			if !ready.Ready {
				return ErrNetworkNotReady
			}
		}
		if next == "pending_entry" || next == "applied" {
			var bound bool
			e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM node_networks WHERE node_id=? AND egress_profile_id=? AND egress_revision=1)`, t.EntryNodeID, t.ProfileID).Scan(&bound)
			if e != nil {
				return e
			}
			if !bound {
				return errors.New("入口绑定已经改变")
			}
			if _, e = tx.ExecContext(ctx, `UPDATE egress_profiles SET enabled=1 WHERE id=? AND enabled=0`, t.ProfileID); e != nil {
				return e
			}
		}
		if next == "stopping_landing" {
			if _, e = tx.ExecContext(ctx, `UPDATE nodes SET revoked=1,enabled=0 WHERE id=? AND source='transit'`, t.LandingNodeID); e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, `INSERT INTO server_network_generations(server_id,generation) VALUES(?,1) ON CONFLICT(server_id) DO UPDATE SET generation=generation+1`, t.LandingServerID); e != nil {
				return e
			}
		}
		if next == "retired" {
			// The agent removes a retained identity only after the final meter
			// ACK and local cleanup have both been persisted. An exact apply
			// receipt alone is insufficient to release the listener reservation.
			var raw string
			if e = tx.QueryRowContext(ctx, `SELECT diagnostics FROM agents WHERE server_id=?`, t.LandingServerID).Scan(&raw); e != nil {
				return e
			}
			var diag agentproto.Diagnostics
			if json.Unmarshal([]byte(raw), &diag) != nil || agentproto.ValidateNetworkDiagnostics(diag) != nil || diag.MeterInventoryVersion != 1 || diag.MeteringError != "" {
				return ErrNetworkConflict
			}
			for _, id := range diag.RetainedNodeMeters {
				if id == t.LandingNodeID {
					return ErrNetworkConflict
				}
			}
		}
		_, e = tx.ExecContext(ctx, `UPDATE managed_transits SET stage=?,updated_at=? WHERE id=? AND stage=?`, next, fmtTime(s.Now()), t.ID, t.Stage)
		if e == nil && next == "retired" {
			// The durable meter identity and transit ledger survive node removal.
			_, e = tx.ExecContext(ctx, `DELETE FROM nodes WHERE id=? AND source='transit' AND revoked=1`, t.LandingNodeID)
		}
		return e
	})
}
func (s *Store) RetireManagedTransit(ctx context.Context, id, expectedImpact string) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		t, e := scanTransit(tx.QueryRowContext(ctx, `SELECT `+transitCols+` FROM managed_transits WHERE id=?`, id))
		if e != nil {
			return e
		}
		if t.Stage == "retired" || t.Stage == "stopping_entry" || t.Stage == "stopping_landing" {
			return nil
		}
		if expectedImpact == "" {
			return ErrNetworkImpactRequired
		}
		preview, e := s.transitRetirementPreview(ctx, tx, t)
		if e != nil {
			return e
		}
		if expectedImpact != preview.Token {
			return ErrNetworkImpactChanged
		}
		if _, e = tx.ExecContext(ctx, `UPDATE egress_profiles SET enabled=0 WHERE id=?`, t.ProfileID); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `UPDATE managed_transits SET stage='stopping_entry',updated_at=? WHERE id=?`, fmtTime(s.Now()), id)
		return e
	})
}
func (s *Store) ManagedTransitProfile(ctx context.Context, q querier, id int64) error {
	var found bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM managed_transits WHERE profile_id=?)`, id).Scan(&found)
	if err != nil {
		return err
	}
	if found {
		return ErrManagedTransit
	}
	return nil
}

func (s *Store) transitServerStatus(ctx context.Context, id int64) string {
	a, err := s.GetAgentByServer(ctx, id)
	if err != nil || a.LastSeenAt == nil || s.Now().Sub(*a.LastSeenAt) > NetworkReadinessMaxAge {
		return "offline"
	}
	if a.ApplyError != "" {
		return "apply_failed"
	}
	var attempts int
	var generation, published int64
	if err = s.db.QueryRowContext(ctx, `SELECT attempts,generation,published_generation FROM server_network_generations WHERE server_id=?`, id).Scan(&attempts, &generation, &published); err != nil {
		return "waiting"
	}
	if attempts >= MaxNetworkPublishAttempts {
		return "publish_failed"
	}
	if generation != published {
		return "publishing"
	}
	latest, err := s.LatestDesiredState(ctx, id)
	if err == nil && a.AppliedRevision == latest.Revision && a.AppliedHash == latest.Hash {
		return "applied"
	}
	return "waiting"
}
func (s *Store) RetryManagedTransit(ctx context.Context, id, expectedUpdatedAt string) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		t, err := scanTransit(tx.QueryRowContext(ctx, `SELECT `+transitCols+` FROM managed_transits WHERE id=?`, id))
		if err != nil {
			return err
		}
		if expectedUpdatedAt == "" || expectedUpdatedAt != t.UpdatedAt {
			return ErrNetworkConflict
		}
		if t.Stage == "applied" || t.Stage == "retired" {
			return fmt.Errorf("%w: 该操作已经完成，无需重试", ErrTransitRequest)
		}
		for _, sid := range []int64{t.EntryServerID, t.LandingServerID} {
			if err = networkMaintenance(ctx, tx, sid); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE server_network_generations SET generation=generation+1 WHERE server_id IN (?,?)`, t.EntryServerID, t.LandingServerID)
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE managed_transits SET updated_at=? WHERE id=?`, fmtTime(s.Now()), id)
		}
		return err
	})
}

func (s *Store) transitEntryReadiness(ctx context.Context, q querier, t ManagedTransit) (NetworkReadiness, error) {
	n, err := s.scanNode(q.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE id=?`, t.EntryNodeID))
	if err != nil {
		return NetworkReadiness{}, err
	}
	if n.Network == nil || n.Network.EgressProfileID != t.ProfileID {
		return NetworkReadiness{}, ErrNetworkConflict
	}
	return s.previewNodeNetworkForEgress(ctx, q, n, n.Network, t.ProfileID)
}
