package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"time"

	"ctlvps/internal/networkconfig"
)

var agentEgressConfigTypes = []struct {
	kind   string
	typeOf reflect.Type
}{
	{"direct", reflect.TypeFor[networkconfig.Direct]()},
	{"socks5", reflect.TypeFor[networkconfig.SOCKS5]()},
	{"ss2022", reflect.TypeFor[networkconfig.SS2022]()},
	{"ssh", reflect.TypeFor[networkconfig.SSH]()},
	{"wireguard", reflect.TypeFor[networkconfig.WireGuard]()},
}

// agentEgressConfigSchema describes the public configuration structs, never
// their values. Credentials stay in the existing separate request property.
func agentEgressConfigSchema() map[string]any {
	variants := []any{}
	byKind := map[string]any{}
	for _, config := range agentEgressConfigTypes {
		shape := agentJSONTypeSchema(config.typeOf, map[reflect.Type]bool{}, 0)
		shape["title"] = config.kind
		variants = append(variants, shape)
		byKind[config.kind] = shape
	}
	return map[string]any{
		"anyOf":          variants,
		"x-kind-schemas": byKind,
		"description":    "按照请求的 kind 使用对应公开配置结构；字段类型来自服务端 networkconfig，必填组合、范围和运行能力由服务端校验。凭据通过独立 credentials 字段提交。",
	}
}

// agentDescribeRequestBody returns a fresh outer schema so registering one
// route cannot mutate the shared generated contract used by another route.
func agentDescribeRequestBody(handler string, body map[string]any) map[string]any {
	if handler == "createShare" || handler == "updateShare" {
		return agentShareEntryRequestSchema(body)
	}
	if handler == "deployNode" || handler == "previewIPv6Node" {
		return agentIPv6DeployRequestSchema(body, handler == "previewIPv6Node")
	}
	if handler == "checkServerIPv6Endpoint" {
		return map[string]any{"type": "object", "required": []string{"host"}, "properties": map[string]any{
			"host": map[string]any{"type": "string", "maxLength": 254, "description": "公网 IPv6 字面量或仅有 AAAA 的公开域名。"},
		}}
	}
	if handler == "createAPIKey" || handler == "updateAPIKeyOperations" {
		result, props := map[string]any{}, map[string]any{}
		for k, v := range body {
			result[k] = v
		}
		if old, ok := body["properties"].(map[string]any); ok {
			for k, v := range old {
				props[k] = v
			}
		}
		props["operations"] = map[string]any{"anyOf": []any{map[string]any{"type": "array", "maxItems": 512, "uniqueItems": true, "items": map[string]any{"type": "string"}}, map[string]any{"type": "null"}}, "description": "能力目录中当前账户可用的非会话专用 operation_id；null 表示全部授权操作，空数组只保留发现接口。"}
		result["properties"] = props
		if handler == "updateAPIKeyOperations" {
			result["required"] = []string{"operations", "password"}
		}
		return result
	}

	if handler == "saveUnavailablePort" {
		return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"transport"}, "properties": map[string]any{
			"transport": map[string]any{"type": "string", "enum": []string{"tcp", "udp", "both"}},
			"network":   map[string]any{"type": "string", "description": "可选，适用的本地网络、运营商或地区，去除首尾空格后最多 120 UTF-8 字节；不需要本地 IP。"},
			"reason":    map[string]any{"type": "string", "description": "可选，人工观察到的不可用原因，去除首尾空格后最多 500 UTF-8 字节。"},
		}, "description": "URL 端口须为 1–65535。同一服务器同一端口只保存一条记录，重复 PUT 更新该条记录；不修改节点或执行网络探测。每台服务器最多 512 条。"}
	}
	if handler == "previewNodeNetwork" || handler == "updateNodeNetwork" {
		result, props := map[string]any{}, map[string]any{}
		for k, v := range body {
			result[k] = v
		}
		if old, ok := body["properties"].(map[string]any); ok {
			for k, v := range old {
				props[k] = v
			}
		}
		props["listen_port"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 65535, "description": "可选。更换监听端口并同步节点连接端口；mieru 至少 1025。须保留原 network、预览并提交 expected_impact。NAT 使用已确认映射，线路占用时拒绝。省略则保留。"}
		props["network"] = map[string]any{"anyOf": []any{agentJSONTypeSchema(reflect.TypeFor[networkconfig.Node](), map[reflect.Type]bool{}, 0), map[string]any{"type": "null"}}, "description": "必须明确提交当前或目标网络策略；null 表示清除绑定。仅换端口时原样保留 GET 节点返回的 network。"}
		result["properties"] = props
		return result
	}
	if handler == "previewAccessRoute" || handler == "createAccessRoute" {
		return agentAccessRouteRequestSchema(handler == "createAccessRoute")
	}
	if handler == "saveServerNAT" {
		return agentNATConfigSchema()
	}
	switch handler {
	case "createEgressProfile", "updateEgressProfile", "previewEgressCreate", "previewEgressProfile":
	default:
		return body
	}
	original, ok := body["properties"].(map[string]any)
	if !ok {
		return body
	}
	result, properties := map[string]any{}, map[string]any{}
	for key, value := range body {
		result[key] = value
	}
	for key, value := range original {
		properties[key] = value
	}
	config := agentEgressConfigSchema()
	properties["config"] = config
	kind := map[string]any{}
	if originalKind, ok := original["kind"].(map[string]any); ok {
		for key, value := range originalKind {
			kind[key] = value
		}
	}
	choices := []string{}
	for _, config := range agentEgressConfigTypes {
		choices = append(choices, config.kind)
	}
	if handler == "previewEgressProfile" {
		// Delete previews allow an explicitly empty kind as well as omission.
		choices = append(choices, "")
	}
	kind["type"], kind["enum"] = "string", choices
	properties["kind"] = kind
	result["properties"] = properties
	conditions := []any{}
	if existing, ok := body["allOf"].([]any); ok {
		conditions = append(conditions, existing...)
	}
	for _, configType := range agentEgressConfigTypes {
		conditions = append(conditions, map[string]any{
			"if": map[string]any{
				// This is a conditional-match guard, not a requirement on
				// the whole request. Omitting kind must not match all branches.
				"required":   []string{"kind"},
				"properties": map[string]any{"kind": map[string]any{"const": configType.kind}},
			},
			"then": map[string]any{"properties": map[string]any{"config": config["x-kind-schemas"].(map[string]any)[configType.kind]}},
		})
	}
	result["allOf"] = conditions
	return result
}

func agentUnresolvedJSONSchema() map[string]any {
	return map[string]any{"description": "由服务端校验", "additionalProperties": true}
}

func agentJSONTypeSchema(typ reflect.Type, seen map[reflect.Type]bool, depth int) map[string]any {
	if typ == nil || depth > 16 || seen[typ] {
		return agentUnresolvedJSONSchema()
	}
	if typ == reflect.TypeFor[time.Time]() {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	if typ == reflect.TypeFor[networkconfig.IngressPolicy]() {
		return map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"mode":           map[string]any{"type": "string", "enum": []string{networkconfig.IngressRelayOnly, networkconfig.IngressDenyCountries}},
				"source_cidrs":   map[string]any{"type": "array", "maxItems": 256, "uniqueItems": true, "items": map[string]any{"type": "string", "description": "规范 IPv4/IPv6 网段；地区拒绝模式中作为中转出口例外。"}},
				"countries":      map[string]any{"type": "array", "maxItems": 64, "uniqueItems": true, "items": map[string]any{"type": "string", "pattern": "^[A-Za-z]{2}$", "description": "ISO 3166-1 alpha-2 国家代码。"}},
				"geo_ipv4_cidrs": map[string]any{"type": "array", "maxItems": 200000, "items": map[string]any{"type": "string"}, "description": "由控制端 preview 返回的固定 GeoIP IPv4 地址集合；不要自行生成。"},
				"geo_ipv6_cidrs": map[string]any{"type": "array", "maxItems": 200000, "items": map[string]any{"type": "string"}, "description": "由控制端 preview 返回的固定 GeoIP IPv6 地址集合；不要自行生成。"},
				"geo_provider":   map[string]any{"type": "string", "enum": []string{networkconfig.GeoIPProviderDBIP, networkconfig.GeoIPProviderMaxMind}},
				"geo_version":    map[string]any{"type": "string", "description": "控制端固定的数据版本。"},
			},
			"description": "relay_only 只允许实际源地址命中 source_cidrs；deny_countries 按控制端物化的 GeoIP 集合拒绝实际源地址。必须先 preview，不能把被中转前的客户端地区当作实际源地址。",
		}
	}
	if typ == reflect.TypeFor[json.RawMessage]() {
		return agentUnresolvedJSONSchema()
	}
	if typ.Kind() == reflect.Pointer {
		return map[string]any{"anyOf": []any{agentJSONTypeSchema(typ.Elem(), seen, depth+1), map[string]any{"type": "null"}}}
	}
	marshaler := reflect.TypeFor[json.Marshaler]()
	if typ.Implements(marshaler) || reflect.PointerTo(typ).Implements(marshaler) {
		// Custom encoders can have a wire shape unrelated to Go fields.
		return agentUnresolvedJSONSchema()
	}
	seen[typ] = true
	defer delete(seen, typ)
	result := map[string]any{}
	switch typ.Kind() {
	case reflect.Bool:
		result["type"] = "boolean"
	case reflect.String:
		result["type"] = "string"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		result["type"] = "integer"
		if typ.Kind() == reflect.Int64 {
			result["format"] = "int64"
		} else if typ.Kind() == reflect.Int32 {
			result["format"] = "int32"
		}
	case reflect.Float32, reflect.Float64:
		result["type"] = "number"
	case reflect.Slice:
		if typ.Elem().Kind() == reflect.Uint8 {
			result["type"], result["contentEncoding"] = "string", "base64"
		} else {
			result["type"], result["items"] = "array", agentJSONTypeSchema(typ.Elem(), seen, depth+1)
		}
	case reflect.Array:
		result["type"], result["items"] = "array", agentJSONTypeSchema(typ.Elem(), seen, depth+1)
	case reflect.Map:
		result["type"], result["additionalProperties"] = "object", agentJSONTypeSchema(typ.Elem(), seen, depth+1)
	case reflect.Struct:
		properties := map[string]any{}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.PkgPath != "" && !field.Anonymous {
				continue
			}
			parts := strings.Split(field.Tag.Get("json"), ",")
			name := parts[0]
			if name == "-" {
				continue
			}
			shape := agentJSONTypeSchema(field.Type, seen, depth+1)
			if field.Anonymous && name == "" {
				if embedded := agentSchemaProperties(shape); embedded != nil {
					for key, value := range embedded {
						properties[key] = value
					}
				}
				continue
			}
			if name == "" {
				name = field.Name
			}
			properties[name] = shape
		}
		result["type"], result["properties"], result["additionalProperties"] = "object", properties, true
	default:
		return agentUnresolvedJSONSchema()
	}
	if typ.PkgPath() != "" && typ.Name() != "" {
		result["x-go-type"] = typ.PkgPath() + "." + typ.Name()
	}
	return result
}

func agentSchemaProperties(shape map[string]any) map[string]any {
	if properties, ok := shape["properties"].(map[string]any); ok {
		return properties
	}
	if variants, ok := shape["anyOf"].([]any); ok {
		for _, variant := range variants {
			if object, ok := variant.(map[string]any); ok {
				if properties := agentSchemaProperties(object); properties != nil {
					return properties
				}
			}
		}
	}
	return nil
}
