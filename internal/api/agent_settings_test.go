package api

import (
	"strings"
	"testing"

	"ctlvps/internal/domain"
)

func TestAgentSettingsContractMatchesEditableWhitelist(t *testing.T) {
	assertAgentSettingsContract(t, agentSettingsContract())
}

// Inspect the registered operation instead of the helper alone, so discovery
// cannot accidentally keep the generic map schema extracted from putSettings.
func TestAgentSettingsDiscoveryIncludesFirstConfigurationKeys(t *testing.T) {
	c := newTestAPI(t)
	for _, operation := range c.api.agentOperations {
		if operation.Method != "PUT" || operation.Path != "/api/v1/settings" {
			continue
		}
		assertAgentSettingsContract(t, operation.RequestBody)
		properties := operation.RequestBody["properties"].(map[string]any)
		for _, key := range []string{domain.SettingTelegramToken, domain.SettingTelegramChatID, domain.SettingSiteURL, domain.SettingSingBoxVersion, domain.SettingSnellVersion, domain.SettingMitaVersion} {
			if properties[key] == nil {
				t.Fatalf("first-time setting %q cannot be discovered", key)
			}
		}
		return
	}
	t.Fatal("settings write operation missing from agent discovery")
}

func assertAgentSettingsContract(t *testing.T, contract map[string]any) {
	t.Helper()
	if contract["type"] != "object" || contract["additionalProperties"] != false {
		t.Fatalf("settings schema does not enforce the real key whitelist: %v", contract)
	}
	if contract["required"] != nil {
		t.Fatal("partial settings updates must not require unchanged keys")
	}
	if description, _ := contract["description"].(string); !strings.Contains(description, "省略未修改项") {
		t.Fatal("settings merge semantics are missing")
	}
	properties, ok := contract["properties"].(map[string]any)
	if !ok {
		t.Fatalf("editable setting names were not exposed: %v", contract)
	}
	count := 0
	for key, editable := range editableSettings {
		if !editable {
			continue
		}
		count++
		field, ok := properties[key].(map[string]any)
		if !ok || field["type"] != "string" {
			t.Fatalf("editable key %q must accept a string value: %v", key, properties[key])
		}
		if field["default"] != nil || field["example"] != nil || field["writeOnly"] != nil {
			t.Fatalf("setting schema invented a value or unreadable property for %q: %v", key, field)
		}
	}
	if len(properties) != count {
		t.Fatalf("schema has %d keys, whitelist allows %d", len(properties), count)
	}
	for key := range properties {
		if !editableSettings[key] {
			t.Fatalf("unsupported setting %q advertised as editable", key)
		}
	}
}
