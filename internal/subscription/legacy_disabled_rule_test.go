package subscription

import (
	"encoding/base64"
	"strings"
	"testing"

	"ctlvps/internal/domain"
	"gopkg.in/yaml.v3"
)

func TestLegacyDisabledRuleStaysCommentedInRenderedTemplate(t *testing.T) {
	oldRule := `{"index":-1,"value":"DOMAIN-SUFFIX,disabled.example,DIRECT","enabled":false}`
	marker := "# vpsct-disabled-rule: " + base64.RawURLEncoding.EncodeToString([]byte(oldRule))
	template := "proxy-groups: []\nrules:\n  " + marker + "\n  - MATCH,DIRECT\n"
	r, err := RenderMihomo(&Bundle{Template: &domain.RuleTemplate{Kind: "mihomo", Content: template}})
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Rules []string `yaml:"rules"`
	}
	if err := yaml.Unmarshal(r.Body, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Rules) != 1 || config.Rules[0] != "MATCH,DIRECT" || !strings.Contains(string(r.Body), marker) {
		t.Fatal("legacy disabled rule changed behavior or was lost")
	}
}
