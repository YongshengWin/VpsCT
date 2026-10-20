package api

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"reflect"
	"runtime"
	"sort"
	"strings"

	"ctlvps/internal/httpx"
	"ctlvps/skills"
)

//go:generate go run ../../scripts/generate-agent-contracts.go

//go:embed agent_contracts.json
var agentContractsJSON []byte

type agentContract struct {
	RequestBody     map[string]any   `json:"request_body,omitempty"`
	QueryParameters []map[string]any `json:"query_parameters"`
	Source          string           `json:"source"`
}

var agentContracts = func() map[string]agentContract {
	var contracts map[string]agentContract
	if err := json.Unmarshal(agentContractsJSON, &contracts); err != nil {
		panic("invalid embedded agent API contracts")
	}
	return contracts
}()

type agentOperation struct {
	OperationID   string           `json:"operation_id"`
	Method        string           `json:"method"`
	Path          string           `json:"path"`
	Summary       string           `json:"summary"`
	Group         string           `json:"group"`
	Handler       string           `json:"handler"`
	RequiredRole  string           `json:"required_role"`
	RequiredScope string           `json:"required_scope"`
	SessionOnly   bool             `json:"session_only"`
	Available     bool             `json:"available"`
	Unavailable   string           `json:"unavailable_reason,omitempty"`
	Reauth        []string         `json:"reauthentication,omitempty"`
	RequestBody   map[string]any   `json:"request_body,omitempty"`
	QueryParams   []map[string]any `json:"query_parameters"`
}

func (a *API) registerAgentOperation(pattern string, policy accessPolicy, h httpx.Handler) {
	method, path, ok := strings.Cut(pattern, " ")
	if !ok {
		panic("agent operation requires an explicit HTTP method")
	}
	name := runtime.FuncForPC(reflect.ValueOf(h).Pointer()).Name()
	if i := strings.LastIndex(name, ")."); i >= 0 {
		name = name[i+2:]
	} else if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Split(strings.TrimSuffix(name, "-fm"), ".")[0]
	role := "user"
	if policy == adminAccess {
		role = "admin"
	}
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/"), "/")
	group := parts[0]
	id := strings.ToLower(method) + "_" + strings.NewReplacer("/", "_", "{", "", "}", "", "-", "_", ".", "_").Replace(strings.TrimPrefix(path, "/api/v1/"))
	contract := agentContracts[name]
	contract.RequestBody = agentDescribeRequestBody(name, contract.RequestBody)
	if name == "putSettings" {
		contract.RequestBody = agentSettingsContract()
	}
	// The legacy upload field exists only so old clients receive an explicit
	// error. A false schema prevents agents from treating it as a capability.
	if properties, ok := contract.RequestBody["properties"].(map[string]any); ok && properties["uploaded_content"] != nil {
		body, fields := map[string]any{}, map[string]any{}
		for field, value := range contract.RequestBody {
			body[field] = value
		}
		for field, value := range properties {
			fields[field] = value
		}
		fields["uploaded_content"] = false
		body["properties"] = fields
		body["description"] = "配置上传已移除，禁止提交 uploaded_content；订阅从节点库生成或转换外部订阅。"
		contract.RequestBody = body
	}
	queries := contract.QueryParameters
	if queries == nil {
		queries = []map[string]any{}
	}
	op := agentOperation{OperationID: id, Method: method, Path: path, Summary: name, Handler: name, Group: group,
		RequiredRole: role, RequiredScope: operationScope(pattern), SessionOnly: sessionOnlyOperation(pattern), RequestBody: contract.RequestBody, QueryParams: queries}
	if method == "POST" && strings.HasSuffix(path, "/maintenance") || path == "/api/v1/api-keys" && method == "POST" {
		op.Reauth = []string{"password", "code_if_2fa_enabled"}
	}
	a.agentOperations = append(a.agentOperations, op)
}

func (a *API) visibleAgentOperations(r *http.Request) []agentOperation {
	u := userFrom(r.Context())
	key := apiKeyFrom(r.Context())
	ops := append([]agentOperation(nil), a.agentOperations...)
	for i := range ops {
		op := &ops[i]
		op.Available = true
		switch {
		case op.RequiredRole == "admin" && !isAdmin(u):
			op.Available, op.Unavailable = false, "admin_required"
		case key != nil && op.SessionOnly:
			op.Available, op.Unavailable = false, "session_required"
		case key != nil && !hasAPIKeyScope(key, op.RequiredScope):
			op.Available, op.Unavailable = false, "insufficient_scope"
		}
	}
	sort.Slice(ops, func(i, j int) bool {
		if ops[i].Path == ops[j].Path {
			return ops[i].Method < ops[j].Method
		}
		return ops[i].Path < ops[j].Path
	})
	return ops
}

func (a *API) agentCapabilities(w http.ResponseWriter, r *http.Request) error {
	u := userFrom(r.Context())
	authentication := "session"
	scopes := []string{"read", "write"}
	var keyID any
	if key := apiKeyFrom(r.Context()); key != nil {
		authentication, scopes, keyID = "api_key", key.Scopes, key.ID
	}
	operations := a.visibleAgentOperations(r)
	httpx.OK(w, map[string]any{
		"product": "VpsCT", "api_version": "v1", "version": a.Config.Version, "base_url": a.baseURL(r),
		"authentication": map[string]any{"type": authentication, "role": u.Role, "scopes": scopes, "api_key_id": keyID, "header": "Authorization: Bearer <API_KEY>"},
		"openapi_url":    "/api/v1/agent/openapi.json", "skill_url": "/api/v1/agent/skill", "skill_bundle_url": "/api/v1/agent/skill.zip", "skill_installer_url": "/agent/skill/install.py", "client_url": "/api/v1/agent/client.py", "reference_url": "/api/v1/agent/reference",
		"limits":     map[string]any{"requests_per_minute_per_key": 240, "requests_per_minute_per_ip": 240, "max_request_bytes": 4 << 20},
		"operations": operations,
		"workflows":  agentWorkflows(operations),
		"deployment": map[string]any{"reference_url": "/api/v1/agent/reference", "reference_section": "2. 部署与恢复", "modes": []string{"native-systemd", "docker-compose"}, "requires_host_authorization": true, "bootstrap_requires_setup_token": true, "api_key_creation": "session_only", "docker_maintenance": "host_compose_script"},
		"notes": []string{
			"API key 继承账户角色及资源权限；read 为 GET/HEAD，其他方法需要 write。",
			"只读响应可能包含节点凭据和订阅链接，请仅向授权用户显示。",
			"API key 管理和两步验证管理需要面板会话；维护写入还需要密码及已启用的两步验证。",
			"写操作没有通用自动重试保证；网络操作和维护使用各自的 operation_id 或 id。",
			"available 表示调用权限，资源归属、运行状态和字段约束由业务接口继续验证。",
		},
	})
	return nil
}

func (a *API) agentOpenAPI(w http.ResponseWriter, r *http.Request) error {
	paths := map[string]any{}
	for _, op := range a.visibleAgentOperations(r) {
		item, ok := paths[op.Path].(map[string]any)
		if !ok {
			item = map[string]any{}
			paths[op.Path] = item
		}
		params := []map[string]any{}
		for _, part := range strings.Split(op.Path, "/") {
			if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
				params = append(params, map[string]any{"name": strings.Trim(part, "{}"), "in": "path", "required": true, "schema": map[string]any{"type": "string"}})
			}
		}
		params = append(params, op.QueryParams...)
		security := []map[string][]string{{"developerKey": {}}}
		if op.SessionOnly {
			security = []map[string][]string{{"sessionCookie": {}}}
		} else {
			security = append(security, map[string][]string{"sessionCookie": {}})
		}
		contentType := "application/json"
		switch op.Path {
		case "/api/v1/events":
			contentType = "text/event-stream"
		case "/api/v1/agent/skill", "/api/v1/agent/reference":
			contentType = "text/markdown"
		case "/api/v1/agent/client.py":
			contentType = "text/x-python"
		case "/api/v1/agent/skill.zip":
			contentType = "application/zip"
		case "/api/v1/connlog/export":
			contentType = "text/csv"
		case "/api/v1/users/{id}/avatar":
			contentType = "image/png"
		}
		description := "成功；响应沿用对应产品接口。创建可能返回 201，异步任务返回 202，无内容操作返回 204。"
		if op.Path == "/api/v1/events" {
			description = "SSE 流；需使用支持 Authorization 请求头的流式 HTTP 客户端。撤销或过期后连接关闭。"
		}
		responses := map[string]any{"2XX": map[string]any{"description": description, "content": map[string]any{contentType: map[string]any{"schema": map[string]any{}}}}, "default": map[string]any{"description": "接口错误；429 时遵循 Retry-After，写入失败须先查询现状。", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/APIError"}}}}}
		if schema := agentRouteResponseSchema(op.Handler); schema != nil {
			status := "200"
			if op.Handler == "createAccessRoute" || op.Handler == "stopAccessRoute" || op.Handler == "retryAccessRoute" {
				status = "202"
			}
			delete(responses, "2XX")
			responses[status] = map[string]any{"description": description, "content": map[string]any{"application/json": map[string]any{"schema": schema}}}
		}
		operation := map[string]any{"operationId": op.OperationID, "summary": op.Summary, "tags": []string{op.Group}, "parameters": params, "security": security, "responses": responses,
			"x-required-role": op.RequiredRole, "x-required-scope": op.RequiredScope, "x-session-only": op.SessionOnly, "x-available": op.Available, "x-reauthentication": op.Reauth}
		if len(op.RequestBody) > 0 {
			operation["requestBody"] = map[string]any{"description": "字段类型从处理器提取，必填组合和业务约束由服务端验证；PUT 不一定表示完整替换。", "content": map[string]any{"application/json": map[string]any{"schema": op.RequestBody}}}
		}
		item[strings.ToLower(op.Method)] = operation
	}
	errorSchema := map[string]any{"type": "object", "properties": map[string]any{
		"error": map[string]any{"type": "object", "properties": map[string]any{
			"code": map[string]string{"type": "string"}, "message": map[string]string{"type": "string"},
		}},
	}}
	components := map[string]any{
		"securitySchemes": map[string]any{
			"developerKey":  map[string]string{"type": "http", "scheme": "bearer", "bearerFormat": "VpsCT API key"},
			"sessionCookie": map[string]string{"type": "apiKey", "in": "cookie", "name": a.sessionName()},
		},
		"schemas": map[string]any{"APIError": errorSchema},
	}
	httpx.OK(w, map[string]any{"openapi": "3.1.0",
		"info":    map[string]any{"title": "VpsCT Agent API", "version": "v1", "description": "与面板共用业务接口。使用 /agent/capabilities 发现权限和能力；API key 的 read/write 权限不改变账户角色与资源归属。"},
		"servers": []map[string]string{{"url": a.baseURL(r)}}, "paths": paths, "components": components,
	})
	return nil
}

func (a *API) agentSkill(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="SKILL.md"`)
	_, err := w.Write(skills.VpsCT)
	return err
}

func (a *API) agentClient(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Content-Type", "text/x-python; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="vpsct.py"`)
	_, err := w.Write(skills.Client)
	return err
}

func (a *API) agentReference(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="api.md"`)
	_, err := w.Write(skills.Reference)
	return err
}

func (a *API) agentSkillBundle(w http.ResponseWriter, r *http.Request) error {
	data, err := agentSkillZIP()
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="vpsct-skill.zip"`)
	_, err = w.Write(data)
	return err
}

func agentSkillZIP() ([]byte, error) {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, file := range []struct {
		name string
		data []byte
	}{
		{"vpsct/SKILL.md", skills.VpsCT},
		{"vpsct/scripts/vpsct.py", skills.Client},
		{"vpsct/references/api.md", skills.Reference},
	} {
		entry, err := z.Create(file.name)
		if err != nil {
			return nil, err
		}
		if _, err := entry.Write(file.data); err != nil {
			return nil, err
		}
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func (a *API) agentSkillInstaller(w http.ResponseWriter, r *http.Request) error {
	data, err := agentSkillZIP()
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	script := bytes.ReplaceAll(skills.Installer, []byte("__VPSCT_SKILL_ZIP_BASE64__"), []byte(base64.StdEncoding.EncodeToString(data)))
	script = bytes.ReplaceAll(script, []byte("__VPSCT_SKILL_ZIP_SHA256__"), []byte(hex.EncodeToString(digest[:])))
	w.Header().Set("Content-Type", "text/x-python; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="install-vpsct-skill.py"`)
	_, err = w.Write(script)
	return err
}
