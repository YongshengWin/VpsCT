package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"ctlvps/internal/domain"
	"ctlvps/internal/httpx"
	"ctlvps/internal/provision"
	"ctlvps/internal/proxynode"
	"ctlvps/internal/store"
	"ctlvps/internal/subscription"
)

func queryInt64Ptr(r *http.Request, name string) *int64 {
	v := r.URL.Query().Get(name)
	if v == "" {
		return nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

// NodeView adds derived display fields.
type NodeView struct {
	Traffic *store.TrafficSummary `json:"traffic,omitempty"`
	domain.Node
	URI            string `json:"uri,omitempty"`
	ServerName     string `json:"server_name,omitempty"`
	ShareName      string `json:"share_name,omitempty"`
	ExternalName   string `json:"external_name,omitempty"`
	ChainFrontName string `json:"chain_front_name,omitempty"`
}

func (a *API) nodeViews(r *http.Request, nodes []domain.Node) []NodeView {
	ctx := r.Context()
	servers := map[int64]string{}
	if list, err := a.Store.ListServers(ctx); err == nil {
		for _, s := range list {
			servers[s.ID] = s.Name
		}
	}
	shares := map[int64]string{}
	if list, err := a.Store.ListShares(ctx, nil); err == nil {
		for _, s := range list {
			shares[s.ID] = s.Name
		}
	}
	exts := map[int64]string{}
	if list, err := a.Store.ListExternal(ctx); err == nil {
		for _, e := range list {
			exts[e.ID] = e.Name
		}
	}
	summaries, summaryErr := a.Store.NodeTrafficSummaries(ctx, 30)
	hosts := a.serverHosts(r)
	out := make([]NodeView, 0, len(nodes))
	for _, n := range nodes {
		v := NodeView{Node: n}
		if n.Source == domain.NodeDeployed && summaryErr == nil {
			summary := summaries[n.ID]
			summary.Days = 30
			v.Traffic = &summary
		}
		if n.ServerID != nil {
			v.ServerName = servers[*n.ServerID]
		}
		if n.ShareID != nil {
			v.ShareName = shares[*n.ShareID]
		}
		if n.ExternalSubID != nil {
			v.ExternalName = exts[*n.ExternalSubID]
		}
		if uri, err := proxynode.ToURI(subscription.ProxyFor(n, hosts)); err == nil {
			v.URI = uri
		}
		out = append(out, v)
	}
	names := map[int64]string{}
	for _, n := range nodes {
		names[n.ID] = n.Name
	}
	for i := range out {
		if out[i].ChainFrontNodeID == nil || *out[i].ChainFrontNodeID == 0 {
			continue
		}
		id := *out[i].ChainFrontNodeID
		if nm, ok := names[id]; ok {
			out[i].ChainFrontName = nm
			continue
		}
		if fn, err := a.Store.GetNode(ctx, id); err == nil {
			out[i].ChainFrontName = fn.Name
		}
	}
	return out
}

func (a *API) serverHosts(r *http.Request) map[int64]string {
	out := map[int64]string{}
	servers, err := a.Store.ListServers(r.Context())
	if err != nil {
		return out
	}
	agents, _ := a.Store.ListAgents(r.Context())
	byServer := map[int64]domain.Agent{}
	for _, ag := range agents {
		byServer[ag.ServerID] = ag
	}
	for _, s := range servers {
		h := s.PublicHost
		if h == "" {
			if ag, ok := byServer[s.ID]; ok {
				h = firstNonEmpty(ag.PublicIPv4, ag.PublicIPv6)
			}
		}
		out[s.ID] = h
	}
	return out
}

func (a *API) listNodes(w http.ResponseWriter, r *http.Request) error {
	f := store.NodeFilter{
		Source:         domain.NodeSource(r.URL.Query().Get("source")),
		ServerID:       queryInt64Ptr(r, "server_id"),
		ExternalSubID:  queryInt64Ptr(r, "external_sub_id"),
		ShareID:        queryInt64Ptr(r, "share_id"),
		IncludeRevoked: r.URL.Query().Get("include_revoked") == "1",
	}
	nodes, err := a.Store.ListNodes(r.Context(), f)
	if err != nil {
		return err
	}
	if q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q"))); q != "" {
		filtered := nodes[:0]
		for _, n := range nodes {
			if strings.Contains(strings.ToLower(n.Name), q) || strings.Contains(strings.ToLower(n.Server), q) || strings.Contains(n.Protocol, q) {
				filtered = append(filtered, n)
			}
		}
		nodes = filtered
	}
	httpx.OK(w, a.nodeViews(r, nodes))
	return nil
}

type nodeInput struct {
	Name             string          `json:"name"`
	Protocol         string          `json:"protocol"`
	Server           string          `json:"server"`
	Port             int             `json:"port"`
	Params           json.RawMessage `json:"params"`
	Tags             []string        `json:"tags"`
	Enabled          *bool           `json:"enabled"`
	URI              string          `json:"uri"` // alternative to fields
	ChainFrontNodeID *int64          `json:"chain_front_node_id"`
}

func (in nodeInput) apply(n *domain.Node) error {
	if in.URI != "" {
		p, err := proxynode.ParseURI(in.URI)
		if err != nil {
			return httpx.BadRequest("链接解析失败: " + err.Error())
		}
		d := p.ToDomain()
		if in.Name != "" {
			d.Name = in.Name
		}
		n.Name, n.Protocol, n.Server, n.Port, n.Params = d.Name, d.Protocol, d.Server, d.Port, d.Params
	} else {
		if in.Name != "" {
			n.Name = strings.TrimSpace(in.Name)
		}
		if in.Protocol != "" {
			n.Protocol = strings.ToLower(in.Protocol)
		}
		if in.Server != "" {
			n.Server = strings.TrimSpace(in.Server)
		}
		if in.Port != 0 {
			n.Port = in.Port
		}
		if len(in.Params) > 0 {
			var m map[string]any
			if err := json.Unmarshal(in.Params, &m); err != nil {
				return httpx.BadRequest("params 必须是 JSON 对象")
			}
			delete(m, "name")
			delete(m, "type")
			delete(m, "server")
			delete(m, "port")
			b, _ := json.Marshal(m)
			n.Params = b
		}
	}
	if n.Name == "" {
		return httpx.BadRequest("名称不能为空")
	}
	if n.Protocol == "" || n.Server == "" || n.Port <= 0 || n.Port > 65535 {
		return httpx.BadRequest("协议、地址和端口不能为空")
	}
	if in.Tags != nil {
		n.Tags = in.Tags
	}
	if in.Enabled != nil {
		n.Enabled = *in.Enabled
	}
	if in.ChainFrontNodeID != nil {
		n.ChainFrontNodeID = normalizeChainFront(in.ChainFrontNodeID)
	}
	return nil
}

func normalizeChainFront(id *int64) *int64 {
	if id == nil || *id == 0 {
		return nil
	}
	return id
}

func (a *API) createNode(w http.ResponseWriter, r *http.Request) error {
	var in nodeInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	n := domain.Node{Source: domain.NodeManual, Enabled: true, Tags: []string{}, OwnerUserID: userFrom(r.Context()).ID}
	if err := in.apply(&n); err != nil {
		return err
	}
	if err := a.Store.CreateNode(r.Context(), &n); err != nil {
		return err
	}
	a.audit(r, "node.create", n.Name, map[string]any{"protocol": n.Protocol})
	httpx.JSON(w, http.StatusCreated, a.nodeViews(r, []domain.Node{n})[0])
	return nil
}

func (a *API) parseNodes(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Text string `json:"text"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	res := proxynode.ParseAny(in.Text)
	out := make([]map[string]any, 0, len(res.Proxies))
	for _, p := range proxynode.DedupeNames(res.Proxies) {
		out = append(out, p.ClashMap())
	}
	httpx.OK(w, map[string]any{"format": res.Format, "proxies": out, "errors": res.Errors})
	return nil
}

func (a *API) importNodes(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Text string   `json:"text"`
		Tags []string `json:"tags"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	res := proxynode.ParseAny(in.Text)
	created := []domain.Node{}
	for _, p := range proxynode.DedupeNames(res.Proxies) {
		n := p.ToDomain()
		n.Source = domain.NodeManual
		n.OwnerUserID = userFrom(r.Context()).ID
		if in.Tags != nil {
			n.Tags = in.Tags
		}
		if err := a.Store.CreateNode(r.Context(), &n); err != nil {
			res.Errors = append(res.Errors, n.Name+": "+err.Error())
			continue
		}
		created = append(created, n)
	}
	a.audit(r, "node.import", "", map[string]any{"count": len(created), "format": res.Format})
	httpx.OK(w, map[string]any{"created": a.nodeViews(r, created), "errors": res.Errors, "format": res.Format})
	return nil
}

func (a *API) reorderNodes(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		IDs []int64 `json:"ids"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := a.Store.ReorderNodes(r.Context(), in.IDs); err != nil {
		return err
	}
	httpx.NoContent(w)
	return nil
}

func (a *API) bulkDeleteNodes(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		IDs []int64 `json:"ids"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	servers := map[int64]bool{}
	n := 0
	for _, id := range in.IDs {
		node, err := a.Store.GetNode(r.Context(), id)
		if err != nil {
			continue
		}
		if node.ShareID != nil {
			continue // share nodes are managed through the share
		}
		if node.ServerID != nil && node.Source == domain.NodeDeployed {
			servers[*node.ServerID] = true
		}
		if err := a.Store.DeleteNode(r.Context(), id); err == nil {
			n++
		}
	}
	for sid := range servers {
		_, _, _ = a.Desired.Publish(r.Context(), sid)
	}
	a.audit(r, "node.bulk_delete", "", map[string]any{"count": n})
	httpx.OK(w, map[string]any{"deleted": n})
	return nil
}

func (a *API) setNodeChain(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name      string `json:"name"`
		FrontID   int64  `json:"front_id"`
		LandingID int64  `json:"landing_id"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.LandingID == 0 {
		return httpx.BadRequest("请选择落地节点")
	}
	landing, err := a.Store.GetNode(r.Context(), in.LandingID)
	if err != nil {
		return httpx.ErrNotFound
	}
	if in.FrontID == 0 {
		if landing.Source == domain.NodeChain {
			if err := a.Store.DeleteNode(r.Context(), landing.ID); err != nil {
				return err
			}
			a.audit(r, "node.unchain", landing.Name, nil)
			httpx.NoContent(w)
			return nil
		}
		chains, _ := a.Store.ListNodes(r.Context(), store.NodeFilter{Source: domain.NodeChain, IncludeRevoked: true})
		for _, ch := range chains {
			if ch.Server == landing.Server && ch.Port == landing.Port {
				_ = a.Store.DeleteNode(r.Context(), ch.ID)
			}
		}
		if landing.ChainFrontNodeID != nil {
			landing.ChainFrontNodeID = nil
			if err := a.Store.UpdateNode(r.Context(), &landing); err != nil {
				return err
			}
		}
		a.audit(r, "node.unchain", landing.Name, nil)
		httpx.OK(w, a.nodeViews(r, []domain.Node{landing})[0])
		return nil
	}
	if in.FrontID == in.LandingID {
		return httpx.BadRequest("前置和落地不能是同一个节点")
	}
	front, err := a.Store.GetNode(r.Context(), in.FrontID)
	if err != nil {
		return httpx.ErrNotFound
	}
	if front.Source == domain.NodeChain || landing.Source == domain.NodeChain {
		return httpx.BadRequest("请选择原始节点作为前置和落地")
	}
	if front.Revoked || landing.Revoked {
		return httpx.BadRequest("已撤销的节点不能组成链式")
	}
	name := strings.TrimSpace(in.Name)
	if existing, err := a.Store.FindChainNode(r.Context(), front.ID, landing.Server, landing.Port); err == nil {
		if name != "" && name != existing.Name {
			existing.Name = name
			if err := a.Store.UpdateNode(r.Context(), &existing); err != nil {
				return err
			}
			a.audit(r, "node.chain", existing.Name, nil)
		}
		httpx.OK(w, a.nodeViews(r, []domain.Node{existing})[0])
		return nil
	}
	if name == "" {
		name = front.Name + " → " + landing.Name
	}
	frontID := front.ID
	ch := domain.Node{
		Name:             name,
		Protocol:         landing.Protocol,
		Server:           landing.Server,
		Port:             landing.Port,
		Params:           append(json.RawMessage(nil), landing.Params...),
		Source:           domain.NodeChain,
		ChainFrontNodeID: &frontID,
		Enabled:          true,
		OwnerUserID:      userFrom(r.Context()).ID,
		Tags:             []string{},
	}
	if err := a.Store.CreateNode(r.Context(), &ch); err != nil {
		return err
	}
	a.audit(r, "node.chain", ch.Name, map[string]any{"front": front.Name, "landing": landing.Name})
	httpx.JSON(w, http.StatusCreated, a.nodeViews(r, []domain.Node{ch})[0])
	return nil
}

func (a *API) getNode(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	n, err := a.Store.GetNode(r.Context(), id)
	if err != nil {
		return httpx.ErrNotFound
	}
	httpx.OK(w, a.nodeViews(r, []domain.Node{n})[0])
	return nil
}

func (a *API) updateNode(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	n, err := a.Store.GetNode(r.Context(), id)
	if err != nil {
		return httpx.ErrNotFound
	}
	var in nodeInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if n.Source == domain.NodeDeployed || n.Source == domain.NodeChain {
		// only cosmetic fields may change on deployed nodes
		if in.Name != "" {
			n.Name = strings.TrimSpace(in.Name)
		}
		if in.Tags != nil {
			n.Tags = in.Tags
		}
		if in.Enabled != nil {
			n.Enabled = *in.Enabled
		}
		if in.ChainFrontNodeID != nil {
			n.ChainFrontNodeID = normalizeChainFront(in.ChainFrontNodeID)
		}
	} else if err := in.apply(&n); err != nil {
		return err
	}
	if err := a.Store.UpdateNode(r.Context(), &n); err != nil {
		return err
	}
	if n.Source == domain.NodeDeployed && n.ServerID != nil {
		_, _, _ = a.Desired.Publish(r.Context(), *n.ServerID)
	}
	a.audit(r, "node.update", n.Name, nil)
	httpx.OK(w, a.nodeViews(r, []domain.Node{n})[0])
	return nil
}

func (a *API) deleteNode(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	n, err := a.Store.GetNode(r.Context(), id)
	if err != nil {
		return httpx.ErrNotFound
	}
	if n.ShareID != nil {
		return httpx.BadRequest("分享节点请在分享中调整目标协议")
	}
	if err := a.Store.DeleteNode(r.Context(), id); err != nil {
		return err
	}
	if n.Source == domain.NodeDeployed && n.ServerID != nil {
		_, _, _ = a.Desired.Publish(r.Context(), *n.ServerID)
	}
	a.audit(r, "node.delete", n.Name, nil)
	httpx.NoContent(w)
	return nil
}

func (a *API) nodeURI(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	n, err := a.Store.GetNode(r.Context(), id)
	if err != nil {
		return httpx.ErrNotFound
	}
	p := subscription.ProxyFor(n, a.serverHosts(r))
	uri, err := proxynode.ToURI(p)
	if err != nil {
		return httpx.BadRequest(err.Error())
	}
	clash := p.ClashMap()
	surge := subscription.SurgeProxyLine(p, "")
	httpx.OK(w, map[string]any{"uri": uri, "clash": clash, "surge": surge})
	return nil
}

func (a *API) nodeTraffic(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	s, err := a.Traffic.Daily(r.Context(), store.SubjectNode, id, httpx.QueryInt(r, "days", 30))
	if err != nil {
		return err
	}
	httpx.OK(w, s)
	return nil
}

func (a *API) regenerateNode(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	n, err := a.Store.GetNode(r.Context(), id)
	if err != nil {
		return httpx.ErrNotFound
	}
	if n.Source != domain.NodeDeployed || n.ServerID == nil {
		return httpx.BadRequest("仅部署节点支持重置凭据")
	}
	s, err := a.Store.GetServer(r.Context(), *n.ServerID)
	if err != nil {
		return httpx.ErrNotFound
	}
	if err := provision.RegenerateCredentials(&n, s); err != nil {
		return err
	}
	if err := a.Store.UpdateNodeCredentials(r.Context(), &n); err != nil {
		return err
	}
	_, _, _ = a.Desired.Publish(r.Context(), s.ID)
	a.audit(r, "node.regenerate", n.Name, nil)
	httpx.OK(w, a.nodeViews(r, []domain.Node{n})[0])
	return nil
}
