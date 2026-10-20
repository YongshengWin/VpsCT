package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNATAgentContractDescribesCASAndStrictTypedMappings(t *testing.T) {
	body := agentDescribeRequestBody("saveServerNAT", map[string]any{"type": "object"})
	if body["additionalProperties"] != false || len(body["required"].([]string)) != 2 {
		t.Fatal("NAT contract lost strict CAS input", body)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"expected_revision"`, `"maximum":9007199254740991`, `"public_port"`, `"local_port"`, `"count"`, `"network"`, `"interface"`, `"local_address"`, `"type":"null"`} {
		if !strings.Contains(string(raw), key) {
			t.Fatal("NAT contract cannot describe accepted configuration", key)
		}
	}
	// Schema callers may annotate their own returned copy; a later operation
	// must retain its independently generated fields.
	delete(body["properties"].(map[string]any), "config")
	if agentNATConfigSchema()["properties"].(map[string]any)["config"] == nil {
		t.Fatal("NAT contracts share mutable outer properties")
	}
}
