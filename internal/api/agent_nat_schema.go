package api

import (
	"reflect"

	"ctlvps/internal/natconfig"
)

// The NAT endpoint decodes a raw object to enforce explicit presence and strict
// field validation. Publish the actual typed configuration for API consumers.
func agentNATConfigSchema() map[string]any {
	config := agentJSONTypeSchema(reflect.TypeFor[natconfig.Config](), map[reflect.Type]bool{}, 0)
	config["additionalProperties"] = false
	config["required"] = []string{"schema", "mappings"}
	properties := config["properties"].(map[string]any)
	properties["schema"] = map[string]any{"type": "integer", "const": 1}
	mappings := properties["mappings"].(map[string]any)
	item := mappings["items"].(map[string]any)
	item["additionalProperties"] = false
	item["required"] = []string{"public_port", "local_port"}
	fields := item["properties"].(map[string]any)
	for _, port := range []string{"public_port", "local_port"} {
		fields[port] = map[string]any{"type": "integer", "minimum": 1, "maximum": 65535}
	}
	fields["count"] = map[string]any{"type": "integer", "minimum": 0, "maximum": natconfig.MaxPortPairs, "default": 1, "description": "连续端口数量；省略或 0 表示 1。全部映射展开后最多 4096 个端口对。"}
	fields["network"] = map[string]any{"type": "string", "enum": []string{"", "tcp", "udp", "both"}, "default": "both"}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"expected_revision", "config"},
		"properties": map[string]any{
			"expected_revision": map[string]any{"type": "integer", "minimum": 0, "maximum": int64(1<<53 - 1), "description": "先读取服务器 NAT 配置，将返回的 revision 原样提交；版本冲突返回 409。"},
			"config":            map[string]any{"anyOf": []any{config, map[string]any{"type": "null"}}, "description": "商家已分配的公网到本机端口映射；null 清除面板配置。本机 root 文件优先且面板只读。"},
		},
	}
}
