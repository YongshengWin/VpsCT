package api

import (
	"maps"
	"reflect"

	"ctlvps/internal/domain"
)

// A protocol profile names exactly one transport. Keep the nested fields
// disjoint so tools cannot submit WS headers on a gRPC endpoint.
func agentTLSChoices() []any {
	var choices []any
	for _, transport := range []string{"tcp", "ws", "grpc"} {
		props := map[string]any{"transport": map[string]any{"const": transport}, "security": map[string]any{"const": "tls"}}
		required := []string{"transport", "security"}
		switch transport {
		case "ws":
			props["ws"] = map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"path": map[string]any{"type": "string", "default": "/", "maxLength": 512, "pattern": `^/[A-Za-z0-9._~/-]*$`, "description": "不能含双斜杠或相对路径段。"},
				"host": map[string]any{"type": "string", "maxLength": 253, "description": "可选 HTTP Host，填写 DNS 域名，与 TLS 域名独立。"},
			}}
		case "grpc":
			required = append(required, "grpc")
			props["grpc"] = map[string]any{"type": "object", "additionalProperties": false, "required": []string{"service_name"}, "properties": map[string]any{"service_name": map[string]any{"type": "string", "pattern": `^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`}}}
		}
		choices = append(choices, map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": props})
	}
	return choices
}

func agentTrojanCombinationSchema(ordinary bool) map[string]any {
	choices := agentTLSChoices()
	if !ordinary {
		choices = choices[:1]
	}
	choices = append(choices, map[string]any{"type": "object", "maxProperties": 0}, map[string]any{"type": "null"})
	return map[string]any{"oneOf": choices, "description": "仅 Trojan。缺省为原生 TCP/TLS；WS 和 gRPC 要求普通接入和 tls_transport_block_reason 为空。"}
}

func agentVLESSCombinationSchema(allowXHTTP bool) map[string]any {
	tcp := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
		"transport": map[string]any{"const": "tcp", "default": "tcp"}, "security": map[string]any{"const": "reality"}, "flow": map[string]any{"const": "xtls-rprx-vision"},
	}}
	choices := []any{tcp, map[string]any{"type": "null"}}
	if allowXHTTP {
		choices = append(choices, agentTLSChoices()...)
		choices = append(choices, map[string]any{"type": "object", "required": []string{"transport"}, "additionalProperties": false, "properties": map[string]any{
			"transport": map[string]any{"const": "xhttp"}, "security": map[string]any{"const": "reality"},
			"xhttp": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"path": map[string]any{"type": "string", "default": "/", "maxLength": 512, "pattern": `^/[A-Za-z0-9._~/-]*$`, "description": "路径段不能是 . 或 ..，不能含双斜杠。"},
				"host": map[string]any{"type": "string", "maxLength": 253, "description": "可选 HTTP Host，仅允许 DNS 域名。"},
				"mode": map[string]any{"type": "string", "default": "auto", "enum": []string{"auto", "packet-up", "stream-up", "stream-one"}},
			}},
		}})
	}
	return map[string]any{"oneOf": choices, "description": "仅 VLESS。省略沿用 TCP + REALITY + Vision。XHTTP 使用普通接入及独立内核，须先读取服务器的 xhttp_block_reason。普通 TLS 的 TCP、WS、gRPC 须读取 tls_transport_block_reason。"}
}

func agentShareEntryRequestSchema(body map[string]any) map[string]any {
	result := maps.Clone(body)
	props := maps.Clone(agentSchemaProperties(body))
	target := agentJSONTypeSchema(reflect.TypeFor[domain.ShareTarget](), map[reflect.Type]bool{}, 0)
	entry := agentJSONTypeSchema(reflect.TypeFor[domain.ShareEntry](), map[reflect.Type]bool{}, 0)
	config := agentJSONTypeSchema(reflect.TypeFor[domain.ShareEntryConfig](), map[reflect.Type]bool{}, 0)
	fields := agentSchemaProperties(config)
	fields["trojan"] = agentTrojanCombinationSchema(true)
	fields["vless"] = agentVLESSCombinationSchema(true)
	fields["domain"] = map[string]any{"type": "string", "maxLength": 253, "description": "普通 TLS 新组合的证书名称与客户端 SNI。省略时使用部署端点。"}
	fields["cert_mode"] = map[string]any{"type": "string", "enum": []string{"", "self_signed", "external", "acme"}, "description": "普通 TLS 新组合的证书模式。空值继承服务器，专属节点首次创建时固定生效值。"}
	fields["cert_id"] = map[string]any{"type": "string", "maxLength": 64, "description": "仅外部证书模式填写已在 VPS 本机登记的证书 ID。"}
	agentSchemaProperties(entry)["id"] = map[string]any{"type": "string", "pattern": "^[a-z0-9][a-z0-9_-]{0,63}$", "description": "服务器内稳定的组合入口标识。"}
	agentSchemaProperties(entry)["snapshot"] = config
	agentSchemaProperties(entry)["quota_bytes"] = map[string]any{"type": "integer", "minimum": 0, "description": "该入口的独立配额，0 表示不限量。"}
	agentSchemaProperties(entry)["node_id"] = map[string]any{"type": "integer", "readOnly": true, "description": "由服务端分配。"}
	entry["required"] = []string{"id", "snapshot"}
	config["required"] = []string{"protocol"}
	agentSchemaProperties(target)["entries"] = map[string]any{"type": "array", "items": entry}
	props["targets"] = map[string]any{"type": "array", "items": target}
	props["entry_version"] = map[string]any{"type": "integer", "enum": []int{0, 1}, "description": "更新已含组合入口的分享时必须为 1，并保留完整目标列表。"}
	result["properties"] = props
	return result
}
