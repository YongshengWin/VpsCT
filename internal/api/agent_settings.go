package api

// agentSettingsContract exposes field names from the same whitelist enforced by
// putSettings. Reading defaults or the stored settings alone cannot discover
// optional settings that have never been configured.
func agentSettingsContract() map[string]any {
	properties := map[string]any{}
	for key, editable := range editableSettings {
		if editable {
			properties[key] = map[string]any{"type": "string"}
		}
	}
	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
		"description":          "只提交本次要修改的设置键，省略未修改项；此接口合并修改项，不需要回传 GET /settings 的整份响应。所有值均为字符串，可用键来自当前服务端可编辑设置白名单。",
	}
}
