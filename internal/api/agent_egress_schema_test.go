package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"testing"
)

func TestAgentEgressConfigurationFieldsAreDiscoverable(t *testing.T) {
	schemas := agentEgressConfigSchema()["x-kind-schemas"].(map[string]any)
	for _, test := range []struct {
		kind   string
		fields map[string]string
	}{
		{"direct", map[string]string{"family": "string", "dns": "object"}},
		{"socks5", map[string]string{"server": "string", "server_port": "integer", "authentication": "string", "udp": "boolean", "outer": "object"}},
		{"ss2022", map[string]string{"server": "string", "server_port": "integer", "method": "string", "outer": "object"}},
		{"ssh", map[string]string{"server": "string", "host_keys": "array", "authentication": "string", "udp": "boolean"}},
		{"wireguard", map[string]string{"public_key": "string", "addresses": "array", "allowed_ips": "array", "mtu": "integer", "reserved": "array"}},
	} {
		t.Run(test.kind, func(t *testing.T) {
			shape := schemas[test.kind].(map[string]any)
			if shape["type"] != "object" || shape["required"] != nil {
				t.Fatalf("configuration type or invented requirement: %v", shape)
			}
			properties := shape["properties"].(map[string]any)
			for key, wireType := range test.fields {
				field, ok := properties[key].(map[string]any)
				if !ok || field["type"] != wireType {
					t.Fatalf("%s.%s is not machine-readable as %s: %v", test.kind, key, wireType, properties[key])
				}
			}
			if properties["password"] != nil || properties["private_key"] != nil || properties["wireguard_private_key"] != nil || properties["Purpose"] != nil || properties["purpose"] != nil {
				t.Fatal("configuration schema contains credentials or JSON-hidden runtime fields")
			}
			assertEgressTypeOnlySchema(t, shape)
		})
	}
	ss2022 := schemas["ss2022"].(map[string]any)["properties"].(map[string]any)
	dns := ss2022["dns"].(map[string]any)["properties"].(map[string]any)
	if dns["transport"].(map[string]any)["type"] != "string" || dns["port"].(map[string]any)["type"] != "integer" {
		t.Fatal("nested resolver fields are not discoverable")
	}
	direct := schemas["direct"].(map[string]any)["properties"].(map[string]any)
	variants := direct["source_ipv4"].(map[string]any)["anyOf"].([]any)
	if variants[1].(map[string]any)["type"] != "null" || variants[0].(map[string]any)["properties"].(map[string]any)["interface_id"] == nil {
		t.Fatal("nullable source-address binding was lost")
	}
}

func TestAgentEgressKindsMatchStoreImplementation(t *testing.T) {
	_, currentFile, _, _ := runtime.Caller(0)
	parsed, err := parser.ParseFile(token.NewFileSet(), filepath.Join(filepath.Dir(currentFile), "../store/egress.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	storeKinds := []string{}
	for _, declaration := range parsed.Decls {
		fn, ok := declaration.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "validateEgress" {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			branch, ok := node.(*ast.SwitchStmt)
			if !ok {
				return true
			}
			name, ok := branch.Tag.(*ast.Ident)
			if !ok || name.Name != "kind" {
				return true
			}
			for _, statement := range branch.Body.List {
				for _, expr := range statement.(*ast.CaseClause).List {
					if literal, ok := expr.(*ast.BasicLit); ok && literal.Kind == token.STRING {
						value, err := strconv.Unquote(literal.Value)
						if err != nil {
							t.Fatal(err)
						}
						storeKinds = append(storeKinds, value)
					}
				}
			}
			return false
		})
	}
	discovered := []string{}
	for _, config := range agentEgressConfigTypes {
		discovered = append(discovered, config.kind)
	}
	sort.Strings(storeKinds)
	sort.Strings(discovered)
	if len(storeKinds) == 0 || !slices.Equal(storeKinds, discovered) {
		t.Fatalf("discovery kinds %v differ from implemented store kinds %v", discovered, storeKinds)
	}
}

func TestAgentEgressFreshInstanceDiscoveryHasTypedConfiguration(t *testing.T) {
	c := newTestAPI(t)
	missing := map[string]bool{"createEgressProfile": true, "updateEgressProfile": true, "previewEgressCreate": true, "previewEgressProfile": true}
	for _, operation := range c.api.agentOperations {
		if !missing[operation.Handler] {
			continue
		}
		delete(missing, operation.Handler)
		properties := operation.RequestBody["properties"].(map[string]any)
		config, ok := properties["config"].(map[string]any)
		if !ok || config["x-kind-schemas"] == nil || len(config["x-kind-schemas"].(map[string]any)) != 5 {
			t.Fatalf("fresh instance omitted typed configs for %s: %v", operation.Handler, properties["config"])
		}
		if operation.RequestBody["required"] != nil {
			t.Fatal("input requirements were invented")
		}
		branches, ok := operation.RequestBody["allOf"].([]any)
		if !ok || len(branches) != 5 {
			t.Fatalf("kind/config association missing for %s", operation.Handler)
		}
		for _, branch := range branches {
			condition := branch.(map[string]any)
			kind := condition["if"].(map[string]any)["properties"].(map[string]any)["kind"].(map[string]any)["const"].(string)
			shape := condition["then"].(map[string]any)["properties"].(map[string]any)["config"].(map[string]any)
			if shape["title"] != kind {
				t.Fatalf("%s selects the wrong config type: %v", kind, shape["title"])
			}
		}
	}
	if len(missing) != 0 {
		t.Fatalf("egress handlers missing from discovery: %v", missing)
	}
}

func TestAgentJSONTypeSchemaTerminatesRecursiveTypes(t *testing.T) {
	type recursive struct {
		Next *recursive `json:"next"`
		Skip string     `json:"-"`
	}
	shape := agentJSONTypeSchema(reflect.TypeFor[recursive](), map[reflect.Type]bool{}, 0)
	properties := shape["properties"].(map[string]any)
	if properties["Skip"] != nil {
		t.Fatal("JSON-hidden field was included")
	}
	variants := properties["next"].(map[string]any)["anyOf"].([]any)
	if variants[0].(map[string]any)["description"] != "由服务端校验" || variants[1].(map[string]any)["type"] != "null" {
		t.Fatal("recursive type did not degrade explicitly or preserve null")
	}
}

func assertEgressTypeOnlySchema(t *testing.T, value any) {
	t.Helper()
	switch value := value.(type) {
	case map[string]any:
		for key, nested := range value {
			switch key {
			case "required", "default", "example", "minimum", "maximum", "minLength", "maxLength", "enum":
				t.Fatalf("type-only schema invented constraint or value %q", key)
			}
			assertEgressTypeOnlySchema(t, nested)
		}
	case []any:
		for _, nested := range value {
			assertEgressTypeOnlySchema(t, nested)
		}
	}
}
