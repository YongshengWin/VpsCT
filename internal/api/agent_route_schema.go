package api

import (
	"maps"
	"reflect"

	"ctlvps/internal/networkconfig"
	"ctlvps/internal/store"
)

func agentIPv6DeployRequestSchema(body map[string]any, preview bool) map[string]any {
	result := maps.Clone(body)
	props := maps.Clone(agentSchemaProperties(body))
	policy := agentJSONTypeSchema(reflect.TypeFor[networkconfig.Node](), map[reflect.Type]bool{}, 0)
	fields := agentSchemaProperties(policy)
	fields["ingress_family"] = map[string]any{"const": "ipv6"}
	fields["listen_mode"] = map[string]any{"type": "string", "enum": []string{"public_ipv6", "address"}}
	fields["advertise_mode"] = map[string]any{"type": "string", "enum": []string{"inherit_ipv6", "override"}}
	fields["on_unavailable"] = map[string]any{"const": "block"}
	policy["required"] = []string{"ingress_family", "listen_mode", "advertise_mode", "on_unavailable"}
	policy["description"] = "仅 IPv6 创建策略。address 要求公网 IPv6 listen_address 和 listen_interface_id。override 要求顶层 advertise_host。初次部署可在完成后再设置出口与来源限制。"
	props["network"] = map[string]any{"anyOf": []any{policy, map[string]any{"type": "null"}}}
	props["ingress_family"] = map[string]any{"type": "string", "enum": []string{"", "default", "ipv6"}}
	props["operation_id"] = map[string]any{"type": "string", "pattern": "^[a-f0-9]{32}$", "description": "仅 IPv6 提交时必填。重试使用同一个 ID 和请求。"}
	props["expected_ipv6"] = map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$", "description": "仅 IPv6 提交时必填。使用最近一次预览的 confirmation 和 port。"}
	props["trojan"] = agentTrojanCombinationSchema(!preview)
	props["vless"] = agentVLESSCombinationSchema(!preview)
	result["properties"] = props
	if preview {
		props["ingress_family"], props["network"] = map[string]any{"const": "ipv6"}, policy
		result["required"] = []string{"protocol", "ingress_family", "network"}
	}
	return result
}

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
	case "getServer":
		shape = agentJSONTypeSchema(reflect.TypeFor[ServerView](), map[reflect.Type]bool{}, 0)
		agentSchemaProperties(shape)["tls_transport_block_reason"] = map[string]any{"type": "string", "description": "空字符串允许新普通 TLS 组合，非空时说明服务器需要满足的条件。"}
	case "listServers":
		shape = agentJSONTypeSchema(reflect.TypeFor[[]ServerView](), map[reflect.Type]bool{}, 0)
	case "renderSubscription":
		shape = agentJSONTypeSchema(reflect.TypeFor[subscriptionRenderResult](), map[reflect.Type]bool{}, 0)
	case "previewSubscription":
		shape = agentJSONTypeSchema(reflect.TypeFor[subscriptionPreviewResult](), map[reflect.Type]bool{}, 0)
	case "getShareByPublicID":
		shape = agentJSONTypeSchema(reflect.TypeFor[ShareView](), map[reflect.Type]bool{}, 0)
		shape["description"] = "按分享的公开标识查询，仍须通过账户或委派授权；标识不是访问凭证。"
	case "previewShareTemplateDraft", "previewAdminShareTemplateDraft":
		shape = agentJSONTypeSchema(reflect.TypeFor[personalTemplateDraftResult](), map[reflect.Type]bool{}, 0)
		shape["description"] = "预览不保存模板；body 包含节点连接信息。nodes 的 source 表示地区来源，groups 表示实际生成的成员。"
	case "listTemplateVersions", "listPersonalTemplateVersions":
		shape = agentJSONTypeSchema(reflect.TypeFor[[]store.TemplateVersion](), map[reflect.Type]bool{}, 0)
	case "setNodeExitRegion", "getNode":
		shape = agentJSONTypeSchema(reflect.TypeFor[NodeView](), map[reflect.Type]bool{}, 0)
		shape["properties"].(map[string]any)["ipv6_ingress_ready"] = map[string]any{"type": "boolean", "description": "仅 IPv6 节点可见。为 true 时，当前配置已收到 IPv6 监听回执并通过地址检查，可导出客户端配置。"}
	case "previewIPv6Node":
		shape = agentJSONTypeSchema(reflect.TypeFor[ipv6DeployPreview](), map[reflect.Type]bool{}, 0)
		shape["description"] = "预检不创建节点。readiness.ready 为 true 时，提交本次 port、confirmation 和固定 operation_id。"
	case "checkServerIPv6Endpoint", "getServerIPv6Endpoint":
		shape = agentJSONTypeSchema(reflect.TypeFor[ipv6EndpointStatus](), map[reflect.Type]bool{}, 0)
		shape["description"] = "检查 IPv6 地址草稿。matched 表示匹配本机可用接口，该操作不保存地址或探测公网端口。"
	case "listUnavailablePorts":
		shape = agentJSONTypeSchema(reflect.TypeFor[[]store.UnavailablePort](), map[reflect.Type]bool{}, 0)
	case "getUnavailablePort", "saveUnavailablePort":
		shape = agentJSONTypeSchema(reflect.TypeFor[store.UnavailablePort](), map[reflect.Type]bool{}, 0)
		shape["description"] = "人工记录，唯一键为 server_id+port。PUT 覆盖同一端口的传输、网络和原因；updated_at 为最后保存时间。记录仅用于提醒，不代表全网封禁，不触发探测或节点配置变更。"
	case "previewAccessRoute":
		shape = agentJSONTypeSchema(reflect.TypeFor[store.AccessRoutePreview](), map[reflect.Type]bool{}, 0)
	case "getAccessRoute", "createAccessRoute", "stopAccessRoute", "retryAccessRoute":
		shape = agentJSONTypeSchema(reflect.TypeFor[store.AccessRoute](), map[reflect.Type]bool{}, 0)
		shape["properties"].(map[string]any)["stage"] = map[string]any{"type": "string", "enum": []string{"pending_landing", "pending_ix", "pending_entry", "applied", "blocked", "stopping_entry", "stopping_ix", "retired"}}
	}
	return shape
}
