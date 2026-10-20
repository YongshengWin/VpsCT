package api

import (
	"strings"
	"testing"
)

// These handlers reuse connlogFilter, but their grouping limits differ from
// that helper's row limit. Discovery must advertise the effective handler
// defaults, or agents requesting the advertised defaults get different results.
func TestAgentContractsPreferHandlerQueryDefaults(t *testing.T) {
	for _, test := range []struct {
		handler string
		limit   float64
	}{
		{"queryConnlog", 100},
		{"topDomains", 50},
		{"connlogSummary", 15},
	} {
		t.Run(test.handler, func(t *testing.T) {
			parameter := contractQuery(t, test.handler, "limit")
			schema := parameter["schema"].(map[string]any)
			if schema["type"] != "integer" || schema["default"] != test.limit {
				t.Fatalf("wrong grouping/row limit contract: %v, want integer default %v", schema, test.limit)
			}
			if !strings.HasPrefix(parameter["x-source"].(string), "internal/api/shares.go:") {
				t.Fatalf("missing query implementation source: %v", parameter)
			}
		})
	}
}

func TestAgentContractsExposeUnclampedPagination(t *testing.T) {
	for _, handler := range []string{"queryConnlog", "listAudit", "accessLog", "getEgressProfile", "serverNetworkOperations", "subscriptionAccessLog"} {
		t.Run(handler, func(t *testing.T) {
			parameter := contractQuery(t, handler, "offset")
			schema := parameter["schema"].(map[string]any)
			if schema["type"] != "integer" || schema["default"] != float64(0) {
				t.Fatalf("pagination offset was omitted or misclassified: %v", schema)
			}
		})
	}
	before := contractQuery(t, "interfaceHistory", "before")["schema"].(map[string]any)
	if before["type"] != "integer" || before["format"] != "int64" || before["default"] != float64(0) {
		t.Fatalf("interface cursor must preserve int64 identity: %v", before)
	}
}

func contractQuery(t *testing.T, handler, name string) map[string]any {
	t.Helper()
	contract, exists := agentContracts[handler]
	if !exists {
		t.Fatalf("handler contract missing: %s", handler)
	}
	for _, parameter := range contract.QueryParameters {
		if parameter["name"] == name {
			if parameter["in"] != "query" || parameter["required"] != false {
				t.Fatalf("unexpected query location or invented requirement: %v", parameter)
			}
			return parameter
		}
	}
	t.Fatalf("query parameter %s missing for %s", name, handler)
	return nil
}
