package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/auth"
	"ctlvps/internal/domain"
	"ctlvps/internal/natconfig"
	"ctlvps/internal/netinventory"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/proxynode"
)

var ErrAccessRoute = errors.New("线路配置无效")
var ErrAccessRouteInUse = errors.New("该服务器或落地节点仍被线路引用，请先停止并完成转发清理")
var ErrRouteOwned = errors.New("该转发由线路维护，请在线路配置中停止并清理")

type AccessRouteRequest struct {
	ID                  string   `json:"operation_id"`
	Name                string   `json:"name"`
	EntryServerID       int64    `json:"entry_server_id"`
	IXServerID          int64    `json:"ix_server_id"`
	LandingNodeID       int64    `json:"landing_node_id"`
	LandingProtocol     string   `json:"landing_protocol,omitempty"`
	LandingSourceHost   string   `json:"landing_source_host,omitempty"`
	EntryPort           int      `json:"entry_port"`
	IXPort              int      `json:"ix_port"`
	AdvertiseHost       string   `json:"advertise_host"`
	IXHost              string   `json:"ix_host"`
	LandingHost         string   `json:"landing_host"`
	LandingPort         int      `json:"landing_port"`
	Network             string   `json:"network"`
	EntryEgressID       int64    `json:"entry_egress_id"`
	EntryEgressRevision int64    `json:"entry_egress_revision"`
	IXEgressID          int64    `json:"ix_egress_id"`
	IXEgressRevision    int64    `json:"ix_egress_revision"`
	SourceMode          string   `json:"source_mode"`
	SourceCIDRs         []string `json:"source_cidrs"`
	ExpectedImpact      string   `json:"expected_impact,omitempty"`
}

type AccessRoute struct {
	LandingNodeName   string             `json:"landing_node_name,omitempty"`
	LandingServerName string             `json:"landing_server_name,omitempty"`
	ID                string             `json:"id"`
	Name              string             `json:"name"`
	Request           AccessRouteRequest `json:"request"`
	EntryForwardID    int64              `json:"entry_forward_id"`
	IXForwardID       int64              `json:"ix_forward_id"`
	SubscriptionID    *int64             `json:"subscription_id"`
	Stage             string             `json:"stage"`
	Message           string             `json:"message"`
	Revision          int64              `json:"revision"`
	EntryStatus       string             `json:"entry_status,omitempty"`
	IXStatus          string             `json:"ix_status,omitempty"`
	CreatedAt         string             `json:"created_at"`
	UpdatedAt         string             `json:"updated_at"`
	requestHash       string
}

type AccessRoutePreview struct {
	Ready                bool                    `json:"ready"`
	Token                string                  `json:"token"`
	Candidate            AccessRouteRequest      `json:"candidate"`
	Entry                NetworkReadiness        `json:"entry"`
	IX                   NetworkReadiness        `json:"ix"`
	Checks               []NetworkReadinessCheck `json:"checks"`
	WaitingAuthorization bool                    `json:"waiting_authorization"`
}

const accessRouteCols = `id,name,request,entry_forward_id,ix_forward_id,subscription_id,stage,message,revision,created_at,updated_at,request_hash`

func scanAccessRoute(row interface{ Scan(...any) error }) (AccessRoute, error) {
	var r AccessRoute
	var raw string
	var sub, ix sql.NullInt64
	err := row.Scan(&r.ID, &r.Name, &raw, &r.EntryForwardID, &ix, &sub, &r.Stage, &r.Message, &r.Revision, &r.CreatedAt, &r.UpdatedAt, &r.requestHash)
	if isNoRows(err) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	err = json.Unmarshal([]byte(raw), &r.Request)
	r.SubscriptionID = intPtr(sub)
	r.IXForwardID = ix.Int64
	return r, err
}
func (s *Store) AccessRoute(ctx context.Context, id string) (AccessRoute, error) {
	return scanAccessRoute(s.db.QueryRowContext(ctx, `SELECT `+accessRouteCols+` FROM access_routes WHERE id=?`, id))
}
func (s *Store) AccessRoutes(ctx context.Context, serverID int64) ([]AccessRoute, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+accessRouteCols+` FROM access_routes WHERE ?=0 OR json_extract(request,'$.entry_server_id')=? OR json_extract(request,'$.ix_server_id')=? OR json_extract(request,'$.landing_node_id') IN (SELECT id FROM nodes WHERE server_id=?) ORDER BY created_at DESC,id LIMIT 256`, serverID, serverID, serverID, serverID)
	if err != nil {
		return nil, err
	}
	out := []AccessRoute{}
	for rows.Next() {
		r, e := scanAccessRoute(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range out {
		_ = s.db.QueryRowContext(ctx, `SELECT n.name,COALESCE(sv.name,'外部节点') FROM nodes n LEFT JOIN servers sv ON sv.id=n.server_id WHERE n.id=?`, out[i].Request.LandingNodeID).Scan(&out[i].LandingNodeName, &out[i].LandingServerName)
		out[i].EntryStatus = s.transitServerStatus(ctx, out[i].Request.EntryServerID)
		if out[i].Request.IXServerID != 0 {
			out[i].IXStatus = s.transitServerStatus(ctx, out[i].Request.IXServerID)
		}
	}
	return out, nil
}

func routeHash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (in AccessRouteRequest) fingerprint() string { in.ExpectedImpact = ""; return routeHash(in) }
func routeForward(in AccessRouteRequest, ix bool) networkconfig.Forward {
	f := networkconfig.Forward{ListenMode: "all", ListenPort: in.EntryPort, Network: in.Network, TargetHost: in.IXHost, TargetPort: in.IXPort, SourceMode: in.SourceMode, SourceCIDRs: in.SourceCIDRs, MaxTCPConnections: 128, EgressProfileID: in.EntryEgressID, EgressRevision: in.EntryEgressRevision}
	if in.IXServerID == 0 {
		f.TargetHost = in.LandingHost
		f.TargetPort = in.LandingPort
	}
	if ix {
		f.ListenPort = in.IXPort
		f.TargetHost = in.LandingHost
		f.TargetPort = in.LandingPort
		f.SourceMode = "all"
		f.SourceCIDRs = []string{}
		f.EgressProfileID = in.IXEgressID
		f.EgressRevision = in.IXEgressRevision
	}
	if in.Network == "both" {
		f.MaxUDPSessions = 128
		f.UDPIdleSeconds = 30
	}
	return f
}

// routeNodeNetworks describes the wire transport, independent of the proxy's
// business UDP support. VLESS UDP, for example, travels inside its TCP transport.
func routeNodeNetworks(n domain.Node) ([]string, error) {
	p := proxynode.FromDomain(n)
	switch n.Protocol {
	case domain.ProtocolVLESS, domain.ProtocolVMess, domain.ProtocolTrojan:
		switch p.Str("network") {
		case "", "tcp", "ws", "httpupgrade", "grpc", "h2", "http":
		default:
			return nil, ErrAccessRoute
		}
	}
	if proxynode.IsSubscriptionInfo(p) {
		return nil, ErrAccessRoute
	}
	if n.Protocol == domain.ProtocolSocks5 || n.Protocol == domain.ProtocolHTTP {
		return []string{"tcp"}, nil
	}
	return natconfig.ProtocolNetworks(n.Protocol, p.Params)
}
func routeSourceValid(n domain.Node, in AccessRouteRequest) bool {
	if !n.Enabled || n.Revoked || n.Port != in.LandingPort {
		return false
	}
	if in.LandingProtocol != "" && n.Protocol != in.LandingProtocol {
		return false
	}
	if in.LandingProtocol == "" && (n.Source != domain.NodeDeployed || n.Protocol != domain.ProtocolShadowsocks) {
		return false
	}
	switch n.Source {
	case domain.NodeDeployed:
		if n.ServerID == nil || *n.ServerID == in.EntryServerID || *n.ServerID == in.IXServerID {
			return false
		}
	case domain.NodeImported, domain.NodeManual:
		if n.ServerID != nil || n.Network != nil || in.LandingSourceHost == "" || n.Server != in.LandingSourceHost {
			return false
		}
	default:
		return false
	}
	networks, err := routeNodeNetworks(n)
	if err != nil {
		return false
	}
	if in.Network == "tcp" && len(networks) == 1 && networks[0] == "udp" {
		return false
	}
	// For SS only, business UDP uses a second wire transport.
	p := proxynode.FromDomain(n)
	if in.Network == "both" && n.Protocol == domain.ProtocolShadowsocks && (p.Params["udp"] == false || p.Str("network") == "tcp") {
		return false
	}
	return true
}
func routeServerHost(ctx context.Context, q querier, id int64) (string, error) {
	var host string
	err := q.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(public_host,''),(SELECT COALESCE(NULLIF(public_ipv4,''),NULLIF(public_ipv6,'')) FROM agents WHERE server_id=servers.id),'') FROM servers WHERE id=? AND enabled=1`, id).Scan(&host)
	if isNoRows(err) {
		err = ErrNotFound
	}
	return host, err
}
func (s *Store) routeFreePort(ctx context.Context, q querier, id int64, wanted int, network string) (int, error) {
	if wanted != 0 {
		if wanted < 1 || wanted > 65535 {
			return 0, fmt.Errorf("%w: 端口须为 1–65535", ErrAccessRoute)
		}
		return wanted, nil
	}
	config, err := s.natPortConfig(ctx, q, id)
	if err != nil {
		return 0, err
	}
	ports := []int{}
	if config != nil {
		pairs, err := config.Expand()
		if err != nil {
			return 0, err
		}
		tcp, udp := map[int]bool{}, map[int]bool{}
		for _, pair := range pairs {
			if pair.Network == "tcp" {
				tcp[pair.PublicPort] = true
			} else {
				udp[pair.PublicPort] = true
			}
		}
		for _, pair := range pairs {
			if pair.Network == "tcp" && pair.PublicPort >= 1025 && (network == "tcp" || udp[pair.PublicPort]) {
				ports = append(ports, pair.PublicPort)
			}
		}
	} else {
		for port := 24000; port < 25000; port++ {
			ports = append(ports, port)
		}
	}
	for _, port := range ports {
		var used bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM server_listener_reservations WHERE server_id=? AND listen_port=?)`, id, port).Scan(&used); err != nil {
			return 0, err
		}
		if !used {
			return port, nil
		}
	}
	return 0, ErrListenPortConflict
}
func routePrivateTargetValid(f networkconfig.Forward, snapshot *agentproto.NetworkSnapshot) bool {
	grant := networkconfig.ForwardGrant{ForwardID: 1, EgressProfileID: f.EgressProfileID, Address: f.TargetHost, Port: f.TargetPort}
	_, err := netinventory.ResolveForwardLiteral(agentproto.ForwardSpec{ForwardID: 1, Config: f, ForwardGrants: []networkconfig.ForwardGrant{grant}}, snapshot)
	return err == nil
}

func (s *Store) routePreview(ctx context.Context, q querier, in AccessRouteRequest) (AccessRoutePreview, error) {
	v := AccessRoutePreview{Ready: true, Checks: []NetworkReadinessCheck{}}
	in.ExpectedImpact = ""
	in.Name = strings.TrimSpace(in.Name)
	if !networkconfig.ValidIdentity(in.ID) || in.Name == "" || len(in.Name) > 100 || strings.ContainsAny(in.Name, "\x00\r\n") || in.EntryServerID < 1 || in.IXServerID < 0 || in.EntryServerID == in.IXServerID || in.LandingNodeID < 1 || in.EntryServerID > 1<<53-1 || in.IXServerID > 1<<53-1 || in.LandingNodeID > 1<<53-1 {
		return v, ErrAccessRoute
	}
	if in.IXServerID == 0 && (in.IXHost != "" || in.IXPort != 0 || in.IXEgressID != 0 || in.IXEgressRevision != 0) {
		return v, fmt.Errorf("%w: 未选择额外中转时不能设置中转地址、端口或出口", ErrAccessRoute)
	}
	if in.Network != "tcp" && in.Network != "both" {
		return v, fmt.Errorf("%w: 连接类型须为 TCP 或 TCP + UDP", ErrAccessRoute)
	}
	if in.SourceMode != "all" && in.SourceMode != "cidr" {
		return v, fmt.Errorf("%w: 请明确允许的客户端来源", ErrAccessRoute)
	}
	n, err := s.scanNode(q.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE id=?`, in.LandingNodeID))
	if err != nil {
		return v, err
	}
	in.LandingProtocol = n.Protocol
	in.LandingSourceHost = n.Server
	host := n.Server
	if n.Source == domain.NodeDeployed && n.ServerID != nil {
		host, err = routeServerHost(ctx, q, *n.ServerID)
		if err != nil {
			return v, err
		}
	}
	if in.LandingHost == "" {
		in.LandingHost = host
	}
	if in.LandingPort == 0 {
		in.LandingPort = n.Port
	}
	if !routeSourceValid(n, in) {
		return v, fmt.Errorf("%w: 请选择有效的受管、导入或手动节点；UDP 传输节点需使用 TCP + UDP，目标端口须与节点一致", ErrAccessRoute)
	}
	if n.Network != nil && n.Network.EgressProfileID != 0 {
		var direct bool
		if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM egress_profiles WHERE id=? AND server_id=? AND kind='direct' AND enabled=1)`, n.Network.EgressProfileID, *n.ServerID).Scan(&direct); err != nil {
			return v, err
		}
		if !direct {
			return v, fmt.Errorf("%w: 落地节点已有隧道出口，请选择直接出网的落地节点", ErrAccessRoute)
		}
	}
	if in.AdvertiseHost == "" {
		in.AdvertiseHost, err = routeServerHost(ctx, q, in.EntryServerID)
		if err != nil {
			return v, err
		}
	}
	if in.IXServerID != 0 && in.IXHost == "" {
		in.IXHost, err = routeServerHost(ctx, q, in.IXServerID)
		if err != nil {
			return v, err
		}
	}
	hosts := []*string{&in.AdvertiseHost, &in.LandingHost}
	if in.IXServerID != 0 {
		hosts = append(hosts, &in.IXHost)
	}
	for _, h := range hosts {
		*h = strings.TrimSpace(*h)
		if err = networkconfig.ValidateAdvertiseHost(*h); err != nil {
			return v, fmt.Errorf("%w: 地址须为单个 IP 或域名", ErrAccessRoute)
		}
	}
	in.EntryPort, err = s.routeFreePort(ctx, q, in.EntryServerID, in.EntryPort, in.Network)
	if err != nil {
		return v, err
	}
	if in.IXServerID != 0 {
		in.IXPort, err = s.routeFreePort(ctx, q, in.IXServerID, in.IXPort, in.Network)
		if err != nil {
			return v, err
		}
	}
	for _, part := range []struct{ id, rev, server int64 }{{in.EntryEgressID, in.EntryEgressRevision, in.EntryServerID}, {in.IXEgressID, in.IXEgressRevision, in.IXServerID}} {
		if part.id == 0 {
			if part.rev != 0 {
				return v, ErrAccessRoute
			}
			continue
		}
		var kind string
		if err = q.QueryRowContext(ctx, `SELECT p.kind FROM egress_profiles p JOIN egress_profile_revisions r ON r.profile_id=p.id AND r.revision=? WHERE p.id=? AND p.server_id=? AND p.enabled=1`, part.rev, part.id, part.server).Scan(&kind); err != nil {
			return v, err
		}
		if kind != "direct" {
			return v, fmt.Errorf("%w: 线路路径只支持宿主机路由或直连网卡出口", ErrAccessRoute)
		}
	}
	for i, server := range routeServers(in) {
		f := routeForward(in, i == 1)
		if err = f.Validate(); err != nil {
			return v, fmt.Errorf("%w: %s", ErrAccessRoute, err)
		}
		p, e := s.previewPortForward(ctx, q, domain.PortForward{ServerID: server}, PortForwardRequest{Action: "create", ServerID: server, Name: in.Name, Enabled: true, Config: &f})
		if e != nil {
			return v, e
		}
		if ip, e := networkconfig.HostAddress(f.TargetHost); e == nil && networkconfig.NeedsTransportGrant(ip) && f.Network == "tcp" {
			st, e := s.networkReadiness(ctx, q, server)
			if e != nil {
				return v, e
			}
			// Only the missing per-resource grant is deferred; local/metadata/unsafe endpoints remain rejected.
			if p.ForwardPrivateVersion == 1 && routePrivateTargetValid(f, st.inventory) {
				for j, c := range p.Checks {
					if c.Code == "forward_target" && !c.Ready {
						p.Checks[j].Message = "创建后等待这台 VPS 的 root 为本线路转发 ID 授权私网目标"
						v.WaitingAuthorization = true
					}
				}
			}
		}
		ready := true
		for _, c := range p.Checks {
			if !c.Ready && !(c.Code == "forward_target" && strings.HasPrefix(c.Message, "创建后等待")) {
				ready = false
			}
		}
		v.Ready = v.Ready && ready
		if i == 0 {
			v.Entry = p
		} else {
			v.IX = p
		}
	}
	v.Candidate = in
	// Bind the preview to current source settings without exposing credentials.
	ixToken := ""
	if v.IX.Impact != nil {
		ixToken = v.IX.Impact.Token
	}
	v.Token = routeHash(struct {
		Candidate AccessRouteRequest
		Entry, IX string
		Source    string
	}{in, v.Entry.Impact.Token, ixToken, routeHash(n)})
	return v, nil
}
func (s *Store) PreviewAccessRoute(ctx context.Context, in AccessRouteRequest) (AccessRoutePreview, error) {
	var v AccessRoutePreview
	err := s.Tx(ctx, func(tx *sql.Tx) error { var e error; v, e = s.routePreview(ctx, tx, in); return e })
	return v, err
}

func (s *Store) CreateAccessRoute(ctx context.Context, in AccessRouteRequest, audit domain.AuditEvent) (AccessRoute, error) {
	var out AccessRoute
	hash := in.fingerprint()
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		old, e := scanAccessRoute(tx.QueryRowContext(ctx, `SELECT `+accessRouteCols+` FROM access_routes WHERE id=?`, in.ID))
		if e == nil {
			if old.requestHash != hash {
				return ErrNetworkOperationConflict
			}
			out = old
			return nil
		}
		if !errors.Is(e, ErrNotFound) {
			return e
		}
		if !validNetworkImpactToken(in.ExpectedImpact) {
			return ErrNetworkImpactRequired
		}
		v, e := s.routePreview(ctx, tx, in)
		if e != nil {
			return e
		}
		if v.Token != in.ExpectedImpact {
			return ErrNetworkImpactChanged
		}
		if !v.Ready {
			return ErrNetworkNotReady
		}
		// Persist the exact candidate used by the preview, not browser defaults.
		candidate := v.Candidate
		var count int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM access_routes`).Scan(&count); e != nil {
			return e
		}
		if count >= 256 {
			return ErrForwardCapacity
		}
		ef, e := s.createPortForward(ctx, tx, domain.PortForward{ServerID: in.EntryServerID, Name: in.Name + " / 入口", Config: routeForward(candidate, false), Enabled: false})
		if e != nil {
			return e
		}
		var ixServer, ixForward any
		if in.IXServerID != 0 {
			xf, e := s.createPortForward(ctx, tx, domain.PortForward{ServerID: in.IXServerID, Name: in.Name + " / 中转", Config: routeForward(candidate, true), Enabled: false})
			if e != nil {
				return e
			}
			ixServer, ixForward = in.IXServerID, xf.ID
		}
		token := auth.NewSubscriptionToken()
		sub := domain.Subscription{Name: in.Name, Kind: domain.SubGenerated, Token: token, TokenHash: auth.HashToken(token), TokenHint: auth.TokenHint(token), Enabled: true, OwnerUserID: 0, NodeSelection: domain.NodeSelection{RouteIDs: []string{in.ID}}}
		if audit.UserID != nil {
			sub.OwnerUserID = *audit.UserID
		}
		if e = s.createSubscription(ctx, tx, &sub); e != nil {
			return e
		}
		now := fmtTime(s.Now())
		raw, _ := json.Marshal(candidate)
		_, e = tx.ExecContext(ctx, `INSERT INTO access_routes(id,request_hash,name,request,entry_server_id,ix_server_id,landing_node_id,entry_forward_id,ix_forward_id,subscription_id,stage,message,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,'pending_landing','等待落地节点及转发条件确认',?,?)`, in.ID, hash, in.Name, string(raw), in.EntryServerID, ixServer, in.LandingNodeID, ef.ID, ixForward, sub.ID, now, now)
		if e != nil {
			return e
		}
		audit.Action, audit.Target = "route.create", in.ID
		audit.Detail = []byte(`{}`)
		if e = s.addAudit(ctx, tx, audit); e != nil {
			return e
		}
		out, e = scanAccessRoute(tx.QueryRowContext(ctx, `SELECT `+accessRouteCols+` FROM access_routes WHERE id=?`, in.ID))
		return e
	})
	return out, err
}

func (s *Store) RequireUnmanagedForward(ctx context.Context, q querier, id int64) error {
	var owned bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM access_routes WHERE entry_forward_id=? OR ix_forward_id=?)`, id, id).Scan(&owned)
	if err != nil {
		return err
	}
	if owned {
		return ErrRouteOwned
	}
	return nil
}

func routeServerApplied(ctx context.Context, q querier, id int64) bool {
	var ok bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agents a JOIN server_network_generations g ON g.server_id=a.server_id JOIN desired_states d ON d.server_id=a.server_id AND d.revision=a.applied_revision AND d.hash=a.applied_hash WHERE a.server_id=? AND a.apply_error='' AND g.generation=g.published_generation AND d.revision=(SELECT MAX(revision) FROM desired_states WHERE server_id=a.server_id))`, id).Scan(&ok)
	return err == nil && ok
}
func (s *Store) routeSource(ctx context.Context, q querier, r AccessRoute) (domain.Node, bool, error) {
	n, err := s.scanNode(q.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE id=?`, r.Request.LandingNodeID))
	if isNoRows(err) {
		return n, false, nil
	}
	if err != nil {
		return n, false, err
	}
	if !routeSourceValid(n, r.Request) {
		return n, false, nil
	}
	if n.Network != nil && n.Network.EgressProfileID != 0 {
		var direct bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM egress_profiles WHERE id=? AND kind='direct' AND enabled=1)`, n.Network.EgressProfileID).Scan(&direct); err != nil {
			return n, false, err
		}
		if !direct {
			return n, false, nil
		}
	}
	if n.ShareID != nil {
		sh, e := scanShare(q.QueryRowContext(ctx, `SELECT `+shareCols+` FROM shares WHERE id=?`, *n.ShareID))
		if e != nil {
			return n, false, e
		}
		if sh.NodeExhausted(n) || sh.Status != domain.ShareActive || (sh.ExpiresAt != nil && !s.Now().Before(*sh.ExpiresAt)) || (sh.QuotaBytes > 0 && domain.QuotaBilled(sh.BillingMode, sh.UsedUpload, sh.UsedDownload) >= sh.QuotaBytes) {
			return n, false, nil
		}
	}
	return s.appliedSubscriptionNode(ctx, q, n)
}
func (s *Store) setRouteMessage(ctx context.Context, q querier, r AccessRoute, stage, message string) error {
	if r.Stage == stage && r.Message == message {
		return nil
	}
	if stage == "retired" {
		if _, err := q.ExecContext(ctx, `UPDATE access_routes SET entry_server_id=NULL,ix_server_id=NULL,landing_node_id=NULL WHERE id=? AND revision=?`, r.ID, r.Revision); err != nil {
			return err
		}
	}
	_, err := q.ExecContext(ctx, `UPDATE access_routes SET stage=?,message=?,revision=revision+1,updated_at=? WHERE id=? AND revision=?`, stage, message, fmtTime(s.Now()), r.ID, r.Revision)
	return err
}
func (s *Store) routeForwardReady(ctx context.Context, q querier, f domain.PortForward) bool {
	v, err := s.previewPortForward(ctx, q, f, PortForwardRequest{Action: "update", ForwardID: f.ID, ExpectedRevision: f.Revision, Enabled: true, Name: f.Name, Config: &f.Config})
	if err != nil {
		return false
	}
	// Liveness loss alone does not prove that an already-applied data path
	// stopped. Local guard leases remain authoritative. Fresh observations
	// are still mandatory for every enable or configuration change.
	for _, check := range v.Checks {
		if check.Ready {
			continue
		}
		switch check.Code {
		case "agent_online", "inventory", "maintenance":
			continue
		}
		return false
	}
	return true
}

func (s *Store) routeSetEnabled(ctx context.Context, tx *sql.Tx, f domain.PortForward, enabled bool) error {
	if f.Enabled == enabled {
		return nil
	}
	if err := networkMaintenance(ctx, tx, f.ServerID); err != nil {
		return err
	}
	if enabled {
		v, e := s.previewPortForward(ctx, tx, f, PortForwardRequest{Action: "update", ForwardID: f.ID, ExpectedRevision: f.Revision, Enabled: true, Name: f.Name, Config: &f.Config})
		if e != nil {
			return e
		}
		if !v.Ready {
			return ErrNetworkNotReady
		}
	}
	_, err := s.updatePortForward(ctx, tx, f.ID, f.Revision, f.Name, enabled, f.Config)
	return err
}

// ReconcileAccessRoute is one durable, restartable stage transition. It rechecks
// exact current receipts and dependency authority inside its write transaction.
func (s *Store) ReconcileAccessRoute(ctx context.Context, id string) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		r, err := scanAccessRoute(tx.QueryRowContext(ctx, `SELECT `+accessRouteCols+` FROM access_routes WHERE id=?`, id))
		if err != nil {
			return err
		}
		if r.Stage == "retired" {
			return nil
		}
		ef, ee := scanForward(tx.QueryRowContext(ctx, `SELECT `+forwardCols+` FROM port_forwards WHERE id=?`, r.EntryForwardID))
		var xf domain.PortForward
		var xe error = ErrNotFound
		if r.IXForwardID != 0 {
			xf, xe = scanForward(tx.QueryRowContext(ctx, `SELECT `+forwardCols+` FROM port_forwards WHERE id=?`, r.IXForwardID))
		}
		if r.Stage == "stopping_entry" {
			if ee != nil && !errors.Is(ee, ErrNotFound) {
				return ee
			}
			if ee == nil {
				return nil
			}
			if r.IXForwardID == 0 {
				return s.setRouteMessage(ctx, tx, r, "retired", "已清理入口转发，落地节点保留")
			}
			if xe != nil && !errors.Is(xe, ErrNotFound) {
				return xe
			}
			if xe == nil && !xf.Retired {
				if err = networkMaintenance(ctx, tx, xf.ServerID); err != nil {
					return err
				}
				if _, err = s.retirePortForward(ctx, tx, xf.ID, xf.Revision); err != nil {
					return err
				}
			}
			return s.setRouteMessage(ctx, tx, r, "stopping_ix", "入口已清理，等待中转 清理与流量结算")
		}
		if r.Stage == "stopping_ix" {
			if errors.Is(xe, ErrNotFound) {
				return s.setRouteMessage(ctx, tx, r, "retired", "已清理两段转发，落地节点保留")
			}
			return xe
		}
		if ee != nil {
			return ee
		}
		if r.IXForwardID != 0 && xe != nil {
			return xe
		}
		if routeHash(ef.Config) != routeHash(routeForward(r.Request, false)) || (r.IXForwardID != 0 && routeHash(xf.Config) != routeHash(routeForward(r.Request, true))) {
			return ErrNetworkConflict
		}
		_, valid, err := s.routeSource(ctx, tx, r)
		if err != nil {
			return err
		}
		if !valid {
			if err = s.routeSetEnabled(ctx, tx, ef, false); err != nil {
				return err
			}
			return s.setRouteMessage(ctx, tx, r, "blocked", "落地节点未应用、已停用、端点已变化或分享失效，入口配置已关闭；请检查源节点后重新配置")
		}
		// Downstream is made ready before the public entry. A missing private grant
		// parks the route here; the local authority remains the only grant issuer.
		if r.IXForwardID != 0 {
			if !xf.Enabled {
				if err = s.routeSetEnabled(ctx, tx, xf, true); err != nil {
					if errors.Is(err, ErrNetworkNotReady) {
						return s.setRouteMessage(ctx, tx, r, "pending_landing", "中转到落地尚未满足条件，请检查该段诊断或本机私网授权")
					}
					return err
				}
				return s.setRouteMessage(ctx, tx, r, "pending_ix", "正在配置中转到落地")
			}
			if !s.routeForwardReady(ctx, tx, xf) {
				if err = s.routeSetEnabled(ctx, tx, ef, false); err != nil {
					return err
				}
				return s.setRouteMessage(ctx, tx, r, "pending_ix", "中转到落地条件已变化，入口关闭；请检查中转诊断、出口或私网授权")
			}
			if !routeServerApplied(ctx, tx, xf.ServerID) {
				if ef.Enabled {
					if err = s.routeSetEnabled(ctx, tx, ef, false); err != nil {
						return err
					}
				}
				return s.setRouteMessage(ctx, tx, r, "pending_ix", "等待中转最新配置应用；入口保持关闭")
			}
		}
		entryPath := "入口到落地"
		if r.IXForwardID != 0 {
			entryPath = "入口到中转"
		}
		if !ef.Enabled {
			if err = s.routeSetEnabled(ctx, tx, ef, true); err != nil {
				if errors.Is(err, ErrNetworkNotReady) {
					return s.setRouteMessage(ctx, tx, r, "pending_entry", entryPath+"尚未满足条件，请检查该段诊断或本机私网授权")
				}
				return err
			}
			return s.setRouteMessage(ctx, tx, r, "pending_entry", "正在配置"+entryPath)
		}
		if !s.routeForwardReady(ctx, tx, ef) {
			if err = s.routeSetEnabled(ctx, tx, ef, false); err != nil {
				return err
			}
			return s.setRouteMessage(ctx, tx, r, "pending_entry", entryPath+"条件已变化，入口关闭；请检查诊断、出口或私网授权")
		}
		if !routeServerApplied(ctx, tx, ef.ServerID) {
			return s.setRouteMessage(ctx, tx, r, "pending_entry", "等待入口最新配置应用")
		}
		return s.setRouteMessage(ctx, tx, r, "applied", "线路配置已应用，请从客户端验证实际线路")
	})
}

func (s *Store) StopAccessRoute(ctx context.Context, id string, revision int64, audit domain.AuditEvent) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		r, err := scanAccessRoute(tx.QueryRowContext(ctx, `SELECT `+accessRouteCols+` FROM access_routes WHERE id=?`, id))
		if err != nil {
			return err
		}
		if r.Stage == "retired" || strings.HasPrefix(r.Stage, "stopping_") {
			return nil
		}
		if r.Revision != revision {
			return ErrNetworkConflict
		}
		f, err := scanForward(tx.QueryRowContext(ctx, `SELECT `+forwardCols+` FROM port_forwards WHERE id=?`, r.EntryForwardID))
		if err != nil {
			return err
		}
		if err = networkMaintenance(ctx, tx, f.ServerID); err != nil {
			return err
		}
		if _, err = s.retirePortForward(ctx, tx, f.ID, f.Revision); err != nil {
			return err
		}
		if err = s.setRouteMessage(ctx, tx, r, "stopping_entry", "正在关闭入口，等待清理与最终计量"); err != nil {
			return err
		}
		audit.Action, audit.Target = "route.stop", id
		audit.Detail = []byte(`{}`)
		return s.addAudit(ctx, tx, audit)
	})
}
func (s *Store) RetryAccessRoute(ctx context.Context, id string, revision int64, audit domain.AuditEvent) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		r, err := scanAccessRoute(tx.QueryRowContext(ctx, `SELECT `+accessRouteCols+` FROM access_routes WHERE id=?`, id))
		if err != nil {
			return err
		}
		if r.Revision != revision || r.Stage == "retired" {
			return ErrNetworkRetryConflict
		}
		for _, sid := range routeServers(r.Request) {
			if err = networkMaintenance(ctx, tx, sid); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE server_network_generations SET generation=generation+1 WHERE server_id IN (?,?)`, r.Request.EntryServerID, r.Request.IXServerID)
		if err != nil {
			return err
		}
		audit.Action, audit.Target = "route.retry", id
		audit.Detail = []byte(`{}`)
		if err = s.addAudit(ctx, tx, audit); err != nil {
			return err
		}
		return s.setRouteMessage(ctx, tx, r, r.Stage, "已提交重试，等待转发配置重新应用")
	})
}

// AccessRouteLanding gates every subscription request against current route
// intent, exact receipts, enabled resources and source-node authority.
func (s *Store) AccessRouteLanding(ctx context.Context, id string) (AccessRoute, domain.Node, bool, error) {
	var r AccessRoute
	var n domain.Node
	valid := false
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		r, err = scanAccessRoute(tx.QueryRowContext(ctx, `SELECT `+accessRouteCols+` FROM access_routes WHERE id=?`, id))
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if r.Stage != "applied" {
			return nil
		}
		forwardIDs := []int64{r.EntryForwardID}
		if r.IXForwardID != 0 {
			forwardIDs = append(forwardIDs, r.IXForwardID)
		}
		for i, fid := range forwardIDs {
			f, e := scanForward(tx.QueryRowContext(ctx, `SELECT `+forwardCols+` FROM port_forwards WHERE id=?`, fid))
			if errors.Is(e, ErrNotFound) {
				return nil
			}
			if e != nil {
				return e
			}
			if !f.Enabled || f.Retired || routeHash(f.Config) != routeHash(routeForward(r.Request, i == 1)) || !routeServerApplied(ctx, tx, f.ServerID) || !s.routeForwardReady(ctx, tx, f) {
				return nil
			}
		}
		n, valid, err = s.routeSource(ctx, tx, r)
		return err
	})
	return r, n, valid, err
}

func requireNoAccessRoutes(ctx context.Context, q querier, id int64, server bool) error {
	clause := "landing_node_id=?"
	args := []any{id}
	if server {
		clause = "entry_server_id=? OR ix_server_id=? OR landing_node_id IN (SELECT id FROM nodes WHERE server_id=?)"
		args = []any{id, id, id}
	}
	var used bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM access_routes WHERE `+clause+`)`, args...).Scan(&used)
	if err != nil && strings.Contains(err.Error(), "no such table: access_routes") {
		var version int
		if e := q.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_version`).Scan(&version); e == nil && version < 40 {
			return nil
		}
	}
	if err != nil {
		return err
	}
	if used {
		return ErrAccessRouteInUse
	}
	return nil
}

// Route failures are reported without storing raw compiler/SQL errors, which
// can originate in credential-bearing configuration builders.
func (s *Store) AccessRouteReconcileFailed(ctx context.Context, id string) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		r, err := scanAccessRoute(tx.QueryRowContext(ctx, `SELECT `+accessRouteCols+` FROM access_routes WHERE id=?`, id))
		if err != nil {
			return err
		}
		if r.Stage == "retired" {
			return nil
		}
		stage := r.Stage
		if !strings.HasPrefix(stage, "stopping_") {
			stage = "blocked"
			if f, e := scanForward(tx.QueryRowContext(ctx, `SELECT `+forwardCols+` FROM port_forwards WHERE id=?`, r.EntryForwardID)); e == nil {
				if e = s.routeSetEnabled(ctx, tx, f, false); e != nil {
					stage = r.Stage
				}
			}
		}
		return s.setRouteMessage(ctx, tx, r, stage, "线路协调暂未完成，请检查线路服务器诊断或维护状态，再重试配置")
	})
}

// A zero relay is an entry-to-landing route; persisted preexisting routes retain their relay.
func routeServers(in AccessRouteRequest) []int64 {
	ids := []int64{in.EntryServerID}
	if in.IXServerID != 0 {
		ids = append(ids, in.IXServerID)
	}
	return ids
}
