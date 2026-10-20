package api

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAgentDiscoveryCoversRoutesAndPermissions(t *testing.T) {
	c := setupAPIKeyTest(t)
	key, _ := issueTestAPIKey(t, c, 1, "read")
	out, raw := keyRequest(t, c, "GET", "/api/v1/agent/capabilities", key, nil, nil, 200)
	if bytes.Contains(raw, []byte(key)) || bytes.Contains(raw, []byte("token_hash")) {
		t.Fatal("discovery disclosed a key")
	}
	ops := out["operations"].([]any)
	if len(ops) != len(c.api.agentOperations) || len(ops) < 100 {
		t.Fatalf("discovery omitted routes: %d/%d", len(ops), len(c.api.agentOperations))
	}
	ids := map[string]bool{}
	for _, value := range ops {
		op := value.(map[string]any)
		id := op["operation_id"].(string)
		if ids[id] {
			t.Fatalf("duplicate operation id %s", id)
		}
		ids[id] = true
		wantAvailable := op["session_only"] == false && op["required_scope"] == "read"
		if op["available"] != wantAvailable {
			t.Fatalf("wrong availability for %s", id)
		}
	}
	doc, _ := keyRequest(t, c, "GET", "/api/v1/agent/openapi.json", key, nil, nil, 200)
	if doc["openapi"] != "3.1.0" {
		t.Fatal("missing OpenAPI version")
	}
	paths := doc["paths"].(map[string]any)
	count := 0
	for _, entry := range paths {
		count += len(entry.(map[string]any))
	}
	if count != len(c.api.agentOperations) {
		t.Fatal("OpenAPI and route catalog differ")
	}
	for path, media := range map[string]string{"/api/v1/connlog/export": "text/csv", "/api/v1/users/{id}/avatar": "image/png", "/api/v1/events": "text/event-stream"} {
		op := paths[path].(map[string]any)["get"].(map[string]any)
		response := op["responses"].(map[string]any)["2XX"].(map[string]any)
		if response["content"].(map[string]any)[media] == nil {
			t.Fatalf("wrong response content type for %s", path)
		}
	}
	for path, method := range map[string]string{"/api/v1/subscriptions": "post", "/api/v1/subscriptions/{id}": "put", "/api/v1/subscriptions/preview": "post"} {
		op := paths[path].(map[string]any)[method].(map[string]any)
		schema := op["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
		if schema["properties"].(map[string]any)["uploaded_content"] != false {
			t.Fatalf("retired upload advertised for %s", path)
		}
	}
	for _, check := range []struct{ path, method, property string }{
		{"/api/v1/servers", "post", "name"},
		{"/api/v1/servers/{id}/nodes", "post", "protocol"},
		{"/api/v1/system/maintenance", "post", "password"},
		{"/api/v1/system/maintenance", "post", "id"},
		{"/api/v1/servers/{id}/forwards", "post", "operation_id"},
		{"/api/v1/api-keys", "post", "scopes"},
	} {
		op := paths[check.path].(map[string]any)[check.method].(map[string]any)
		body, ok := op["requestBody"].(map[string]any)
		if !ok {
			t.Fatalf("missing request schema for %s %s", check.method, check.path)
		}
		schema := body["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
		properties, ok := schema["properties"].(map[string]any)
		if !ok || properties[check.property] == nil {
			t.Fatalf("missing %s for %s %s: %v", check.property, check.method, check.path, schema)
		}
	}
	for _, path := range []string{"/api/v1/agent/skill", "/api/v1/agent/client.py", "/api/v1/agent/reference"} {
		_, data := keyRequest(t, c, "GET", path, key, nil, nil, 200)
		if len(data) < 100 || bytes.Contains(data, []byte(key)) {
			t.Fatalf("missing or unsafe downloadable artifact %s", path)
		}
	}
	_, data := keyRequest(t, c, "GET", "/api/v1/agent/skill.zip", key, nil, nil, 200)
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(z.File) != 3 || z.File[0].Name != "vpsct/SKILL.md" || z.File[1].Name != "vpsct/scripts/vpsct.py" || z.File[2].Name != "vpsct/references/api.md" {
		t.Fatal("skill archive is missing its executable client or reference")
	}
}

func TestAgentSkillInstallerIsPublicAndMatchesAuthenticatedBundle(t *testing.T) {
	c := setupAPIKeyTest(t)
	response, err := http.Get(c.srv.URL + "/agent/skill/install.py")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	script, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/x-python") {
		t.Fatalf("installer unavailable without credentials: %d", response.StatusCode)
	}
	key, _ := issueTestAPIKey(t, c, 1, "read")
	_, bundle := keyRequest(t, c, "GET", "/api/v1/agent/skill.zip", key, nil, nil, 200)
	digest := sha256.Sum256(bundle)
	if !bytes.Contains(script, []byte(base64.StdEncoding.EncodeToString(bundle))) || !bytes.Contains(script, []byte(hex.EncodeToString(digest[:]))) {
		t.Fatal("installer and deployed skill bundle differ")
	}
	if bytes.Contains(script, []byte("__VPSCT_SKILL_ZIP_")) || bytes.Contains(script, []byte(key)) {
		t.Fatal("installer contains unresolved placeholders or credentials")
	}
	for _, op := range c.api.agentOperations {
		if op.Path == "/agent/skill/install.py" {
			t.Fatal("public installer registered as authenticated business operation")
		}
	}
}

func TestAgentRequestContractsMatchRegisteredHandlers(t *testing.T) {
	c := newTestAPI(t)
	missing := []string{}
	for _, op := range c.api.agentOperations {
		if _, ok := agentContracts[op.Handler]; !ok {
			missing = append(missing, op.Method+" "+op.Path+" ("+op.Handler+")")
		}
	}
	if len(missing) != 0 {
		t.Fatalf("missing generated handler contracts:\n%s", strings.Join(missing, "\n"))
	}
	// The request types follow JSON encoding, including inherited maintenance
	// fields and nullable update fields; they do not invent required constraints.
	for _, name := range []string{"createServer", "updateServer", "maintenanceAuth", "readPortForwardRequest"} {
		contract := agentContracts[name]
		if contract.RequestBody["required"] != nil {
			t.Fatalf("generator invented required constraints for %s", name)
		}
		if _, err := json.Marshal(contract.RequestBody); err != nil {
			t.Fatal(err)
		}
	}
}
