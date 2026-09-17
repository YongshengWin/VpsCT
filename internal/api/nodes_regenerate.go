package api

import (
	"net/http"
	"sort"

	"ctlvps/internal/domain"
	"ctlvps/internal/httpx"
	"ctlvps/internal/provision"
)

type nodeResetResult struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type serverResetResult struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Published bool   `json:"published"`
	Revision  int64  `json:"revision"`
}

// bulkRegenerateNodes rotates each distinct eligible node once, then publishes
// one configuration per affected server. Publishing is not agent acknowledgement.
func (a *API) bulkRegenerateNodes(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		IDs []int64 `json:"ids"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if len(in.IDs) == 0 || len(in.IDs) > 500 {
		return httpx.BadRequest("请选择 1–500 个节点")
	}
	results := make([]nodeResetResult, 0, len(in.IDs))
	servers := map[int64]domain.Server{}
	seen := map[int64]bool{}
	rotated := 0
	for _, id := range in.IDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		result := nodeResetResult{ID: id, Status: "failed"}
		node, err := a.Store.GetNode(r.Context(), id)
		if err != nil {
			result.Message = "节点不存在或读取失败"
			results = append(results, result)
			continue
		}
		result.Name = node.Name
		if node.Source != domain.NodeDeployed || node.ServerID == nil || node.Revoked {
			result.Status = "skipped"
			result.Message = "仅支持未撤销的已部署节点；链式节点请重置其原始节点"
			results = append(results, result)
			continue
		}
		if err := a.requireNoMaintenance(r, *node.ServerID); err != nil {
			result.Message = "服务器正在维护或无法检查维护状态，请稍后重试"
			results = append(results, result)
			continue
		}
		server, err := a.Store.GetServer(r.Context(), *node.ServerID)
		if err != nil {
			result.Message = "无法读取所属服务器"
		} else if err := provision.RegenerateCredentials(&node, server); err != nil {
			result.Message = "生成新凭据失败"
		} else if err := a.Store.UpdateNodeCredentials(r.Context(), &node); err != nil {
			result.Message = "保存新凭据失败"
		} else {
			result.Status = "rotated"
			result.Message = "凭据已更新；服务器应用配置后生效"
			servers[server.ID] = server
			rotated++
		}
		results = append(results, result)
	}
	serverResults := make([]serverResetResult, 0, len(servers))
	ids := make([]int64, 0, len(servers))
	for id := range servers {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		ds, _, err := a.Desired.Publish(r.Context(), id)
		serverResults = append(serverResults, serverResetResult{ID: id, Name: servers[id].Name, Published: err == nil, Revision: ds.Revision})
	}
	a.audit(r, "node.bulk_regenerate", "", map[string]any{"rotated": rotated, "results": results, "servers": serverResults})
	httpx.OK(w, map[string]any{"rotated": rotated, "results": results, "servers": serverResults})
	return nil
}
