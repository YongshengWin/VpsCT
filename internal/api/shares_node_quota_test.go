package api

import (
	"ctlvps/internal/domain"
	"testing"
)

func TestShareInputNodeQuotas(t *testing.T) {
	for _, tc := range []struct {
		name   string
		quotas map[string]int64
		valid  bool
	}{
		{"unlimited", nil, true}, {"per entry", map[string]int64{"ss": 100, "vless": 200}, true},
		{"negative", map[string]int64{"ss": -1}, false}, {"unselected", map[string]int64{"snell": 100}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sh := domain.Share{}
			err := (shareInput{Name: "quota-fixture", Targets: []domain.ShareTarget{{ServerID: 1, Protocols: []string{"ss", "vless"}, QuotaBytes: tc.quotas}}}).apply(&sh)
			if (err == nil) != tc.valid {
				t.Fatal("node quota validation", err)
			}
			if tc.valid && sh.Targets[0].QuotaBytes["ss"] != tc.quotas["ss"] {
				t.Fatal("node quota lost")
			}
		})
	}
}
