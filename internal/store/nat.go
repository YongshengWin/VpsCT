package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/natconfig"
	"ctlvps/internal/networkconfig"
)

var ErrNATRevision = errors.New("NAT 配置已变化，请刷新后重试")
var ErrNATMappingRequired = errors.New("请先在商家面板做好端口映射，再配置并等待 agent 应用后部署节点")
var ErrNATNotReady = errors.New("NAT 配置尚未由在线 agent 确认，暂不能分配节点端口")
var ErrNATEdit = errors.New("NAT 配置条件未满足")
var ErrNATResources = errors.New("NAT 配置与现有资源冲突")
var ErrNATPublicHost = errors.New("NAT 公网入口无效")

type ServerNATView struct {
	ServerID      int64                 `json:"server_id"`
	Revision      int64                 `json:"revision"`
	DesiredConfig *natconfig.Config     `json:"desired_config"`
	Report        *agentproto.NATStatus `json:"report"`
	Editable      bool                  `json:"editable"`
	Reason        string                `json:"reason,omitempty"`
	Pending       bool                  `json:"pending"`
	invalidReport bool
}

func normalizeNAT(c *natconfig.Config) (*natconfig.Config, error) {
	if c == nil {
		return nil, nil
	}
	b, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	n, err := natconfig.Decode(b)
	return &n, err
}

func sameNAT(a, b *natconfig.Config) bool {
	na, ea := normalizeNAT(a)
	nb, eb := normalizeNAT(b)
	if ea != nil || eb != nil {
		return false
	}
	ba, _ := json.Marshal(na)
	bb, _ := json.Marshal(nb)
	return string(ba) == string(bb)
}

func controllerNAT(ctx context.Context, q querier, serverID int64) (*natconfig.Config, int64, error) {
	var raw sql.NullString
	var revision int64
	err := q.QueryRowContext(ctx, `SELECT revision,config FROM server_nat_configs WHERE server_id=?`, serverID).Scan(&revision, &raw)
	if isNoRows(err) {
		return nil, 0, nil
	}
	// Historical migration fixtures (and only databases predating this table)
	// represent the legacy contract; a current database missing it must fail.
	if err != nil && strings.Contains(err.Error(), "no such table: server_nat_configs") {
		var version int
		if e := q.QueryRowContext(ctx, `SELECT max(version) FROM schema_version`).Scan(&version); e == nil && version < 36 {
			return nil, 0, nil
		}
	}
	if err != nil || !raw.Valid {
		return nil, revision, err
	}
	c, err := natconfig.Decode([]byte(raw.String))
	return &c, revision, err
}

// ControllerNAT is the persisted intent, never the selected local authority.
func (s *Store) ControllerNAT(ctx context.Context, serverID int64) (*natconfig.Config, int64, error) {
	return controllerNAT(ctx, s.db, serverID)
}

func (s *Store) serverNAT(ctx context.Context, q querier, serverID int64) (ServerNATView, error) {
	v := ServerNATView{ServerID: serverID}
	var exists int
	if err := q.QueryRowContext(ctx, `SELECT 1 FROM servers WHERE id=?`, serverID).Scan(&exists); err != nil {
		if isNoRows(err) {
			return v, ErrNotFound
		}
		return v, err
	}
	var err error
	v.DesiredConfig, v.Revision, err = controllerNAT(ctx, q, serverID)
	if err != nil {
		return v, err
	}
	a, err := scanAgent(q.QueryRowContext(ctx, `SELECT `+agentCols+` FROM agents WHERE server_id=?`, serverID))
	if err != nil && !isNoRows(err) {
		return v, err
	}
	var d agentproto.Diagnostics
	if json.Unmarshal(a.Diagnostics, &d) == nil && d.NAT != nil {
		if agentproto.ValidateNAT(d.NAT) == nil {
			v.Report = d.NAT
		} else {
			v.invalidReport = true
		}
	}
	v.Pending = v.DesiredConfig != nil
	if v.Report != nil {
		if v.Report.Source == "local" {
			v.Pending = false
		} else {
			v.Pending = !sameNAT(v.DesiredConfig, v.Report.Config) || v.Report.Status == "pending"
		}
	}
	switch {
	case v.Report != nil && v.Report.Source == "local":
		v.Reason = "本机 root NAT 文件优先，面板只读"
	case v.Report == nil:
		v.Reason = "请先升级并等待 agent 上报 NAT v2 能力"
	case a.TokenHash == "" || a.LastSeenAt == nil || s.Now().Sub(*a.LastSeenAt) < -time.Second || s.Now().Sub(*a.LastSeenAt) > NetworkReadinessMaxAge:
		v.Reason = "需要最近两分钟内上报的已注册 agent"
	case d.SecurityVersion < 1 || !d.SecurityPolicy || d.SecurityPaused || !d.NetworkConfigureAllowed:
		v.Reason = "本机安全策略必须允许 agent.configure 且未暂停变更"
	default:
		var active int
		if err = q.QueryRowContext(ctx, `SELECT count(*) FROM maintenance_jobs WHERE server_id=? AND status IN ('queued','running')`, serverID).Scan(&active); err != nil {
			return v, err
		}
		if active > 0 {
			v.Reason = "等待服务器维护任务结束后才能修改 NAT"
		} else {
			v.Editable = true
		}
	}
	return v, nil
}

func (s *Store) ServerNAT(ctx context.Context, serverID int64) (ServerNATView, error) {
	var v ServerNATView
	err := s.Tx(ctx, func(tx *sql.Tx) error { var err error; v, err = s.serverNAT(ctx, tx, serverID); return err })
	return v, err
}

func validateNATPublicHost(host string) error {
	if err := networkconfig.ValidateAdvertiseHost(host); err != nil {
		return errors.New("启用 NAT 前须显式填写商家的公网 IPv4 地址或访问域名")
	}
	if ip, err := netip.ParseAddr(host); err == nil && (!ip.Is4() || !ip.IsGlobalUnicast() || natPrivateAddress(ip) || netip.MustParsePrefix("0.0.0.0/8").Contains(ip) || netip.MustParsePrefix("169.254.0.0/16").Contains(ip) || netip.MustParsePrefix("240.0.0.0/4").Contains(ip)) {
		return errors.New("NAT 公网入口只支持公网 IPv4 地址或访问域名")
	}
	return nil
}

func (s *Store) SaveServerNAT(ctx context.Context, serverID, expected int64, config *natconfig.Config) (ServerNATView, error) {
	c, err := normalizeNAT(config)
	if err != nil {
		return ServerNATView{}, err
	}
	var out ServerNATView
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		v, err := s.serverNAT(ctx, tx, serverID)
		if err != nil {
			return err
		}
		if !v.Editable {
			return fmt.Errorf("%w: %s", ErrNATEdit, v.Reason)
		}
		if expected < 0 || expected != v.Revision {
			return ErrNATRevision
		}
		if sameNAT(c, v.DesiredConfig) {
			out = v
			return nil
		}
		if c == nil && ((v.DesiredConfig != nil && len(v.DesiredConfig.Mappings) > 0) || (v.Report != nil && v.Report.Configured)) {
			if err = s.validateNATResources(ctx, tx, serverID, natconfig.Config{Schema: 1}); err != nil {
				return err
			}
		}
		if c != nil {
			var host string
			if err = tx.QueryRowContext(ctx, `SELECT public_host FROM servers WHERE id=?`, serverID).Scan(&host); err != nil {
				return err
			}
			if len(c.Mappings) > 0 {
				if err = validateNATPublicHost(host); err != nil {
					return fmt.Errorf("%w: %v", ErrNATPublicHost, err)
				}
			}
			if len(c.Mappings) > 0 || (v.DesiredConfig != nil && len(v.DesiredConfig.Mappings) > 0) || (v.Report != nil && v.Report.Configured) {
				if err = s.validateNATResources(ctx, tx, serverID, *c); err != nil {
					return err
				}
			}
		}
		var raw any
		if c != nil {
			b, _ := json.Marshal(c)
			raw = string(b)
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO server_nat_configs(server_id,revision,config,updated_at) VALUES(?,1,?,?) ON CONFLICT(server_id) DO UPDATE SET revision=revision+1,config=excluded.config,updated_at=excluded.updated_at WHERE server_nat_configs.revision=?`, serverID, raw, fmtTime(s.Now()), expected)
		if err != nil {
			return err
		}
		count, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return ErrNATRevision
		}
		out, err = s.serverNAT(ctx, tx, serverID)
		return err
	})
	if err != nil && (strings.Contains(err.Error(), "SQLITE_BUSY") || strings.Contains(err.Error(), "database is locked")) {
		err = ErrNATRevision
	}
	return out, err
}

func (s *Store) validateNATResources(ctx context.Context, q querier, serverID int64, c natconfig.Config) error {
	pairs, err := c.Expand()
	if err != nil {
		return err
	}
	rows, err := q.QueryContext(ctx, `SELECT listen_port FROM server_listener_reservations WHERE server_id=?`, serverID)
	if err != nil {
		return err
	}
	used := map[int]bool{}
	for rows.Next() {
		var p int
		if err = rows.Scan(&p); err != nil {
			rows.Close()
			return err
		}
		used[p] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range pairs {
		if p.PublicPort != p.LocalPort && used[p.LocalPort] {
			return fmt.Errorf("%w: NAT 本地端口 %d 与已保留的节点或转发监听冲突", ErrNATResources, p.LocalPort)
		}
	}
	rows, err = q.QueryContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE server_id=? AND source='deployed' AND revoked=0 AND enabled=1`, serverID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		n, err := s.scanNode(rows)
		if err != nil {
			return err
		}
		if natPrivateNode(n) {
			continue
		}
		var params map[string]any
		if err = json.Unmarshal(n.ServerParams, &params); err != nil {
			return err
		}
		networks, err := natconfig.ProtocolNetworks(n.Protocol, params)
		if err != nil {
			return err
		}
		id, port := n.ID, n.ListenPort
		ok, err := c.SupportsPort(port, networks)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: NAT 配置缺少现有节点 %d 的公网端口 %d 或必要传输", ErrNATResources, id, port)
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	rows.Close()
	rows, err = q.QueryContext(ctx, `SELECT `+forwardCols+` FROM port_forwards WHERE server_id=? AND retired=0 AND enabled=1`, serverID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		f, err := scanForward(rows)
		if err != nil {
			return err
		}
		if natPrivateForward(f.Config) {
			continue
		}
		id, port := f.ID, f.Config.ListenPort
		networks := natForwardNetworks(f.Config.Network)
		ok, err := c.SupportsPort(port, networks)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: NAT 配置缺少现有转发 %d 的公网端口 %d 或必要传输", ErrNATResources, id, port)
		}
	}
	return rows.Err()
}

// NATPortConfig returns only confirmed effective authority. A pending controller
// change or malformed/stale local report cannot silently fall back to randomness.
func (s *Store) natPortConfig(ctx context.Context, q querier, serverID int64) (*natconfig.Config, error) {
	v, err := s.serverNAT(ctx, q, serverID)
	if err != nil {
		return nil, err
	}
	var declared int
	if err := q.QueryRowContext(ctx, `SELECT is_nat FROM servers WHERE id=?`, serverID).Scan(&declared); err != nil {
		var version int
		if !strings.Contains(err.Error(), "no such column: is_nat") || q.QueryRowContext(ctx, `SELECT max(version) FROM schema_version`).Scan(&version) != nil || version >= 37 {
			return nil, err
		}
	}
	if declared == 1 && (v.Report == nil || !v.Report.Configured || v.Report.Config == nil || len(v.Report.Config.Mappings) == 0) {
		return nil, ErrNATMappingRequired
	}
	known := v.DesiredConfig != nil || v.invalidReport || (v.Report != nil && (v.Report.Source != "none" || v.Report.Configured || v.Report.Status == "error" || v.Report.Status == "pending"))
	if !known {
		return nil, nil
	}
	if v.Report == nil || v.Pending || (v.Report.Status != "applied" && v.Report.Status != "disabled") {
		return nil, ErrNATNotReady
	}
	var a domain.Agent
	a, err = scanAgent(q.QueryRowContext(ctx, `SELECT `+agentCols+` FROM agents WHERE server_id=?`, serverID))
	if err != nil {
		return nil, err
	}
	var d agentproto.Diagnostics
	if json.Unmarshal(a.Diagnostics, &d) != nil || a.TokenHash == "" || a.LastSeenAt == nil || s.Now().Sub(*a.LastSeenAt) < -time.Second || s.Now().Sub(*a.LastSeenAt) > NetworkReadinessMaxAge || d.SecurityVersion < 1 || !d.SecurityPolicy || d.SecurityPaused || !d.NetworkConfigureAllowed {
		return nil, ErrNATNotReady
	}
	var active int
	if err = q.QueryRowContext(ctx, `SELECT count(*) FROM maintenance_jobs WHERE server_id=? AND status IN ('queued','running')`, serverID).Scan(&active); err != nil {
		return nil, err
	}
	if active != 0 {
		return nil, ErrNATNotReady
	}
	if v.Report.Status == "disabled" && !v.Report.Configured {
		return nil, nil
	}
	if !v.Report.Configured {
		return nil, ErrNATNotReady
	}
	return v.Report.Config, nil
}

func (s *Store) NATPortConfig(ctx context.Context, serverID int64) (*natconfig.Config, error) {
	var c *natconfig.Config
	err := s.Tx(ctx, func(tx *sql.Tx) error { var err error; c, err = s.natPortConfig(ctx, tx, serverID); return err })
	return c, err
}

func (s *Store) validateNATNode(ctx context.Context, q querier, n domain.Node) error {
	if n.Source != domain.NodeDeployed || n.ServerID == nil {
		return nil
	}
	c, err := s.natPortConfig(ctx, q, *n.ServerID)
	if err != nil || c == nil {
		return err
	}
	if err = natCheckLocalPort(*c, n.ListenPort); err != nil {
		return err
	}
	if natPrivateNode(n) {
		return nil
	}
	if err = s.natPublicHost(ctx, q, *n.ServerID); err != nil {
		return err
	}
	var params map[string]any
	if err = json.Unmarshal(n.ServerParams, &params); err != nil {
		return err
	}
	networks, err := natconfig.ProtocolNetworks(n.Protocol, params)
	if err != nil {
		return err
	}
	ok, err := c.SupportsPort(n.ListenPort, networks)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: 节点端口未由商家映射完整的协议传输", ErrNATResources)
	}
	return nil
}

func natPrivateAddress(ip netip.Addr) bool {
	return ip.IsPrivate() || netip.MustParsePrefix("100.64.0.0/10").Contains(ip)
}

func natPrivateNode(n domain.Node) bool {
	ip, err := netip.ParseAddr(n.Server)
	return n.Network != nil && n.Network.AdvertiseMode == "override" && err == nil && natPrivateAddress(ip)
}

func natPrivateForward(f networkconfig.Forward) bool {
	ip, err := netip.ParseAddr(f.ListenAddress)
	if f.ListenMode != "address" || err != nil || !natPrivateAddress(ip) || f.SourceMode != "cidr" || len(f.SourceCIDRs) == 0 {
		return false
	}
	for _, raw := range f.SourceCIDRs {
		p, err := netip.ParsePrefix(raw)
		if err != nil {
			return false
		}
		private := false
		for _, rawRange := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "fc00::/7"} {
			r := netip.MustParsePrefix(rawRange)
			if p.Addr().BitLen() == r.Addr().BitLen() && p.Bits() >= r.Bits() && r.Contains(p.Addr()) {
				private = true
				break
			}
		}
		if !private {
			return false
		}
	}
	return true
}

func natForwardNetworks(network string) []string {
	if network == "both" {
		return []string{"tcp", "udp"}
	}
	return []string{network}
}

func natCheckLocalPort(c natconfig.Config, port int) error {
	pairs, err := c.Expand()
	if err != nil {
		return err
	}
	for _, p := range pairs {
		if p.LocalPort != p.PublicPort && p.LocalPort == port {
			return fmt.Errorf("%w: 监听端口与 NAT 本地映射入口冲突", ErrNATResources)
		}
	}
	return nil
}

func (s *Store) natPublicHost(ctx context.Context, q querier, serverID int64) error {
	var host string
	if err := q.QueryRowContext(ctx, `SELECT public_host FROM servers WHERE id=?`, serverID).Scan(&host); err != nil {
		return err
	}
	if err := validateNATPublicHost(host); err != nil {
		return fmt.Errorf("%w: %v", ErrNATPublicHost, err)
	}
	return nil
}

func (s *Store) validateNATForward(ctx context.Context, q querier, f domain.PortForward) error {
	if !f.Enabled || f.Retired {
		return nil
	}
	c, err := s.natPortConfig(ctx, q, f.ServerID)
	if err != nil || c == nil {
		return err
	}
	if err = natCheckLocalPort(*c, f.Config.ListenPort); err != nil {
		return err
	}
	if natPrivateForward(f.Config) {
		return nil
	}
	if err = s.natPublicHost(ctx, q, f.ServerID); err != nil {
		return err
	}
	ok, err := c.SupportsPort(f.Config.ListenPort, natForwardNetworks(f.Config.Network))
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: 转发端口未由商家映射完整的协议传输", ErrNATResources)
	}
	return nil
}

func natNodeTransportsEqual(a, b domain.Node) bool {
	var ap, bp map[string]any
	if json.Unmarshal(a.ServerParams, &ap) != nil || json.Unmarshal(b.ServerParams, &bp) != nil {
		return false
	}
	an, ae := natconfig.ProtocolNetworks(a.Protocol, ap)
	bn, be := natconfig.ProtocolNetworks(b.Protocol, bp)
	return ae == nil && be == nil && slices.Equal(an, bn)
}
