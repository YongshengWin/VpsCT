package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
)

var ErrNetworkImpactRequired = errors.New("请先预览本次变更并核对影响范围")
var ErrNetworkImpactChanged = errors.New("变更的依赖或影响范围已变化，请重新预览后提交")
var ErrNetworkImpactCapacity = errors.New("该服务器的节点超过单次网络影响预览上限")

// Keep the preview bounded by the same node capacity as a desired payload.
const MaxNetworkImpactNodes = 2048

// Impact contains administrative configuration metadata only. It never copies
// client credentials, ServerParams, raw desired payloads or diagnostic errors.
type NetworkImpactNode struct {
	NodeID          int64       `json:"node_id"`
	Name            string      `json:"name"`
	Protocol        string      `json:"protocol"`
	Core            domain.Core `json:"core"`
	ListenPort      int         `json:"listen_port"`
	Enabled         bool        `json:"enabled"`
	Revoked         bool        `json:"revoked"`
	ShareID         int64       `json:"share_id,omitempty"`
	ShareName       string      `json:"share_name,omitempty"`
	ShareStatus     string      `json:"share_status,omitempty"`
	NetworkRevision int64       `json:"network_revision"`
	EgressProfileID int64       `json:"egress_profile_id,omitempty"`
	EgressRevision  int64       `json:"egress_revision,omitempty"`
	EgressEnabled   bool        `json:"egress_enabled"`
	Blocked         bool        `json:"blocked"` // desired configuration, not live state
	Effect          string      `json:"effect"`  // binding | clear | disable | resume | pinned | restart
	RestartPossible bool        `json:"restart_possible"`
}

type NetworkImpact struct {
	HistoryComplete    bool                       `json:"history_complete"`
	HistoricalNodes    []NetworkHistoricalNode    `json:"historical_nodes"`
	HistoricalForwards []NetworkHistoricalForward `json:"historical_forwards,omitempty"`
	Token              string                     `json:"token"`
	ServerID           int64                      `json:"server_id"`
	ServerEnabled      bool                       `json:"server_enabled"`
	RuntimeChange      bool                       `json:"runtime_change"`
	ReferenceCount     int                        `json:"reference_count"`
	RestartCount       int                        `json:"restart_count"`
	RestartScope       string                     `json:"restart_scope"`
	BeforeListen       string                     `json:"before_listen,omitempty"`
	AfterListen        string                     `json:"after_listen,omitempty"`
	BeforeHost         string                     `json:"before_host,omitempty"`
	AfterHost          string                     `json:"after_host,omitempty"`
	Nodes              []NetworkImpactNode        `json:"nodes"`
	Forwards           []NetworkImpactForward     `json:"forwards,omitempty"`
}

func validNetworkImpactToken(token string) bool {
	b, err := hex.DecodeString(token)
	return err == nil && len(b) == sha256.Size && hex.EncodeToString(b) == token
}

func impactToken(v NetworkImpact, candidate any) (string, error) {
	v.Token = ""
	b, err := json.Marshal(struct {
		Version   int           `json:"version"`
		Impact    NetworkImpact `json:"impact"`
		Candidate any           `json:"candidate"`
	}{1, v, candidate})
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

func (s *Store) impactNodes(ctx context.Context, q querier, server domain.Server) ([]NetworkImpactNode, error) {
	rows, err := q.QueryContext(ctx, `SELECT n.id,n.name,n.protocol,n.listen_port,n.enabled,n.revoked,
 COALESCE(n.share_id,0),COALESCE(sh.name,''),COALESCE(sh.status,''),COALESCE(b.revision,0),
 COALESCE(b.egress_profile_id,0),COALESCE(b.egress_revision,0),COALESCE(p.enabled,1)
 FROM nodes n LEFT JOIN shares sh ON sh.id=n.share_id LEFT JOIN node_networks b ON b.node_id=n.id
 LEFT JOIN egress_profiles p ON p.id=b.egress_profile_id
 WHERE n.server_id=? AND n.source IN ('deployed','transit') ORDER BY n.id LIMIT ?`, server.ID, MaxNetworkImpactNodes+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NetworkImpactNode{}
	for rows.Next() {
		var n NetworkImpactNode
		if err := rows.Scan(&n.NodeID, &n.Name, &n.Protocol, &n.ListenPort, &n.Enabled, &n.Revoked, &n.ShareID, &n.ShareName, &n.ShareStatus, &n.NetworkRevision, &n.EgressProfileID, &n.EgressRevision, &n.EgressEnabled); err != nil {
			return nil, err
		}
		n.Core = domain.CoreFor(n.Protocol, server.CoreMode)
		n.Blocked = !server.Enabled || !n.Enabled || n.Revoked || !n.EgressEnabled || (n.ShareStatus != "" && n.ShareStatus != string(domain.ShareActive))
		out = append(out, n)
		if len(out) > MaxNetworkImpactNodes {
			return nil, ErrNetworkImpactCapacity
		}
	}
	return out, rows.Err()
}

func (s *Store) impactServer(ctx context.Context, q querier, serverID int64) (domain.Server, error) {
	server, err := scanServer(q.QueryRowContext(ctx, `SELECT `+serverCols+` FROM servers WHERE id=?`, serverID))
	if isNoRows(err) {
		err = ErrNotFound
	}
	return server, err
}

func listenDescription(policy *networkconfig.Node) string {
	if policy != nil && policy.ListenMode == "address" {
		return policy.ListenAddress
	}
	return "全部本机地址"
}

func (s *Store) nodeNetworkImpact(ctx context.Context, q querier, n domain.Node, in NodeNetworkRequest) (NetworkImpact, error) {
	v := NetworkImpact{ServerID: *n.ServerID, RuntimeChange: true, ReferenceCount: 1, Nodes: []NetworkImpactNode{}, BeforeListen: listenDescription(n.Network), AfterListen: listenDescription(in.Network), BeforeHost: n.Server}
	server, err := s.impactServer(ctx, q, *n.ServerID)
	if err != nil {
		return v, err
	}
	v.ServerEnabled = server.Enabled
	v.AfterHost, err = s.nodeNetworkHost(ctx, q, n, in.Network, in.AdvertiseHost)
	if err != nil {
		return v, err
	}
	nodes, err := s.impactNodes(ctx, q, server)
	if err != nil {
		return v, err
	}
	core := domain.CoreFor(n.Protocol, server.CoreMode)
	for _, item := range nodes {
		if item.NodeID == n.ID {
			item.Effect = "binding"
			if in.Network == nil {
				item.Effect = "clear"
			}
		} else if item.Core == core && core == domain.CoreSingBox && !item.Revoked {
			item.Effect = "restart"
		} else {
			continue
		}
		item.RestartPossible = !item.Revoked && (item.Core == domain.CoreSingBox || item.NodeID == n.ID)
		if item.RestartPossible {
			v.RestartCount++
		}
		v.Nodes = append(v.Nodes, item)
	}
	if core == domain.CoreSingBox {
		v.RestartScope = "server_singbox"
	}
	if err = s.addNodeForwardImpact(ctx, q, &v, core); err != nil {
		return v, err
	}
	if err = s.addImpactHistory(ctx, q, &v); err != nil {
		return v, err
	}
	// Binding intent includes its pinned version and the resolved inherited
	// endpoint. A changed draft cannot reuse an unrelated preview digest.
	in.ID, in.ExpectedImpact = "", ""
	in.ExpectedRevision = n.NetworkRevision
	v.Token, err = impactToken(v, in)
	return v, err
}

func (s *Store) egressNetworkImpact(ctx context.Context, q querier, p domain.EgressProfile, in EgressProfileRequest) (NetworkImpact, error) {
	v := NetworkImpact{ServerID: p.ServerID, Nodes: []NetworkImpactNode{}}
	server, err := s.impactServer(ctx, q, p.ServerID)
	if err != nil {
		return v, err
	}
	v.ServerEnabled = server.Enabled
	// A new template has no consumers, so creation never scans or changes
	// unrelated deployment state.
	if in.Action != "create" {
		nodes, err := s.impactNodes(ctx, q, server)
		if err != nil {
			return v, err
		}
		forwards, err := s.impactForwards(ctx, q, p.ServerID)
		if err != nil {
			return v, err
		}
		for _, f := range forwards {
			if f.EgressProfileID == p.ID {
				v.ReferenceCount++
			}
		}
		for _, n := range nodes {
			if n.EgressProfileID == p.ID {
				v.ReferenceCount++
			}
		}
		v.RuntimeChange = in.Action == "update" && p.Enabled != in.Enabled && v.ReferenceCount > 0
		for _, f := range forwards {
			if f.EgressProfileID == p.ID {
				f.Effect = "pinned"
				if v.RuntimeChange {
					f.Effect = "disable"
					if in.Enabled {
						f.Effect = "resume"
					}
				}
			} else if v.RuntimeChange && !f.Retired {
				f.Effect = "restart"
			} else {
				continue
			}
			f.RestartPossible = v.RuntimeChange && !f.Retired
			if f.RestartPossible {
				v.RestartCount++
			}
			v.Forwards = append(v.Forwards, f)
		}
		for _, n := range nodes {
			if n.EgressProfileID == p.ID {
				n.Effect = "pinned"
				if v.RuntimeChange {
					if in.Enabled {
						n.Effect = "resume"
					} else {
						n.Effect = "disable"
					}
				}
			} else if v.RuntimeChange && n.Core == domain.CoreSingBox && !n.Revoked {
				n.Effect = "restart"
			} else {
				continue
			}
			n.RestartPossible = v.RuntimeChange && n.Core == domain.CoreSingBox && !n.Revoked
			if n.RestartPossible {
				v.RestartCount++
			}
			v.Nodes = append(v.Nodes, n)
		}
	}
	if v.RuntimeChange {
		v.RestartScope = "server_singbox"
	}
	if err = s.addImpactHistory(ctx, q, &v); err != nil {
		return v, err
	}
	in.ID, in.ExpectedImpact = "", ""
	v.Token, err = impactToken(v, in)
	return v, err
}

// ReviewNodeNetwork reads admission and dependency scope from one snapshot.
// It is not a runtime lease; both are checked again in the writer transaction.
func (s *Store) ReviewNodeNetwork(ctx context.Context, id int64, policy *networkconfig.Node, host *string) (NetworkReadiness, error) {
	var v NetworkReadiness
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		n, err := s.scanNode(tx.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE id=?`, id))
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		v, err = s.previewNodeNetwork(ctx, tx, n, policy)
		if err != nil {
			return err
		}
		impact, err := s.nodeNetworkImpact(ctx, tx, n, NodeNetworkRequest{NodeID: id, Network: policy, AdvertiseHost: host})
		if err != nil {
			return err
		}
		v.Impact = &impact
		return nil
	})
	return v, err
}

type EgressPreview struct {
	Ready  bool          `json:"ready"`
	Issues []string      `json:"issues"`
	Impact NetworkImpact `json:"impact"`
}

func (s *Store) PreviewEgressProfile(ctx context.Context, in EgressProfileRequest) (EgressPreview, error) {
	v := EgressPreview{Ready: true, Issues: []string{}}
	// Preview has no operation identity and does not reserve one.
	in.ID = "00000000000000000000000000000000"
	in.ExpectedImpact = ""
	if _, err := in.normalize(); err != nil {
		return v, err
	}
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		p := domain.EgressProfile{ServerID: in.ServerID}
		var err error
		if in.Action != "create" {
			p, err = scanEgress(tx.QueryRowContext(ctx, `SELECT `+egressCols+` FROM egress_profiles WHERE id=?`, in.ProfileID))
			if err != nil {
				return err
			}
			if p.CurrentRevision != in.ExpectedRevision {
				return ErrNetworkConflict
			}
			if in.Action == "update" && p.Kind != in.Kind {
				return fmt.Errorf("%w: 出口类型不能在原配置上更换", ErrEgressRequest)
			}
		}
		if in.Action != "delete" {
			var previous *domain.EgressProfile
			if in.Action == "update" {
				previous = &p
			}
			if _, err = s.prepareEgressCredentials(ctx, tx, in.Kind, in.Config, in.Credentials, previous); err != nil {
				return fmt.Errorf("%w: %s", ErrEgressRequest, err)
			}
		}
		v.Impact, err = s.egressNetworkImpact(ctx, tx, p, in)
		if err != nil {
			return err
		}
		if err = networkMaintenance(ctx, tx, p.ServerID); err != nil {
			if !errors.Is(err, ErrNetworkMaintenance) {
				return err
			}
			v.Issues = append(v.Issues, err.Error())
		}
		if in.Action == "delete" && v.Impact.ReferenceCount > 0 {
			v.Issues = append(v.Issues, ErrEgressInUse.Error())
		}
		if v.Impact.RuntimeChange && in.Enabled {
			for _, item := range v.Impact.Nodes {
				if item.EgressProfileID != p.ID {
					continue
				}
				n, err := s.scanNode(tx.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE id=?`, item.NodeID))
				if err != nil {
					return err
				}
				ready, err := s.previewNodeNetworkForEgress(ctx, tx, n, n.Network, p.ID)
				if err != nil {
					return err
				}
				for _, check := range ready.Checks {
					if !check.Ready {
						v.Issues = append(v.Issues, fmt.Sprintf("节点 #%d：%s", item.NodeID, check.Message))
					}
				}
			}
		}
		v.Ready = len(v.Issues) == 0
		return nil
	})
	return v, err
}
