package api

import (
	"ctlvps/internal/store"
	"reflect"
)

func agentAccessRouteRequestSchema(create bool) map[string]any {
	shape := agentJSONTypeSchema(reflect.TypeFor[store.AccessRouteRequest](), map[reflect.Type]bool{}, 0)
	p := shape["properties"].(map[string]any)
	p["operation_id"] = map[string]any{"type": "string", "pattern": "^[a-f0-9]{32}$", "description": "一次创建固定一个随机编号。预览和提交使用同一编号；结果未知时先 GET 该编号，不生成新编号重复创建。"}
	p["network"] = map[string]any{"type": "string", "enum": []string{"tcp", "both"}, "description": "落地节点的线缆传输类型；VLESS 的 UDP 业务可能仍通过 TCP 传输，不能根据业务 UDP 推断。"}
	p["source_mode"] = map[string]any{"type": "string", "enum": []string{"all", "cidr"}}
	for _, field := range []string{"entry_port", "ix_port", "landing_port"} {
		p[field] = map[string]any{"type": "integer", "minimum": 0, "maximum": 65535, "description": "0 表示由预览分配或从落地节点读取；提交时保留 candidate 中的实际值。"}
	}
	p["ix_server_id"] = map[string]any{"type": "integer", "minimum": 0, "maximum": int64(1<<53 - 1), "description": "中转服务器 ID；0 或省略表示入口直接连落地，两台结构。三台结构须显式指定额外中转，不能与入口或受管落地相同。未选中转时 ix_host/ix_port/ix_egress_id/ix_egress_revision 必须为空或 0。"}
	p["landing_node_id"].(map[string]any)["description"] = "受管、导入或手动节点 ID；协议、端点和可用性由预览检查。落地可直接运行在第 2 台机器上。"
	p["expected_impact"] = map[string]any{"type": "string", "description": "提交时使用最近一次预览的 token；其余字段原样使用 candidate。409/428 后重新读取和预览。"}
	required := []string{"operation_id", "name", "entry_server_id", "landing_node_id", "network", "source_mode"}
	if create {
		required = append(required, "expected_impact")
	}
	shape["required"] = required
	shape["description"] = "线路不会创建物理专线。网络与路由须预先接通，私网授权按各 VPS 本机策略处理。不要手工改动由线路管理的底层转发。"
	return shape
}

func agentRouteResponseSchema(handler string) map[string]any {
	var shape map[string]any
	switch handler {
	case "previewAccessRoute":
		shape = agentJSONTypeSchema(reflect.TypeFor[store.AccessRoutePreview](), map[reflect.Type]bool{}, 0)
	case "getAccessRoute", "createAccessRoute", "stopAccessRoute", "retryAccessRoute":
		shape = agentJSONTypeSchema(reflect.TypeFor[store.AccessRoute](), map[reflect.Type]bool{}, 0)
		shape["properties"].(map[string]any)["stage"] = map[string]any{"type": "string", "enum": []string{"pending_landing", "pending_ix", "pending_entry", "applied", "blocked", "stopping_entry", "stopping_ix", "retired"}}
	}
	return shape
}
