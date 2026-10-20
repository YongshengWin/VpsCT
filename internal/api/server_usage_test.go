package api

import (
	"fmt"
	"testing"
	"time"

	"ctlvps/internal/store"
)

func TestServerUsageCalibrationAndCurrentPeriodAPI(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	c.api.Store.Now = func() time.Time { return now }
	c.api.Traffic.Now = c.api.Store.Now
	fresh := c.do("POST", "/api/v1/servers", map[string]any{"name": "fresh", "quota_bytes": 100, "quota_reset_day": 8, "quota_calibrated_usage_bytes": 40}, 201)
	if freshUsage := fresh["usage"].(map[string]any); freshUsage["total"] != float64(0) || freshUsage["billed"] != float64(40) {
		t.Fatal("new server calibration was not applied", freshUsage)
	}
	srv := c.do("POST", "/api/v1/servers", map[string]any{"name": "fixture", "quota_bytes": 200, "quota_reset_day": 8}, 201)
	id := int64(srv["id"].(float64))
	base := fmt.Sprintf("/api/v1/servers/%d", id)
	if err := c.api.Store.AddTraffic(c.api.ctx(), store.SubjectServer, id, time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC), 50, 62); err != nil {
		t.Fatal(err)
	}
	short := c.do("GET", base+"/traffic?days=1", nil, 200)
	period := c.do("GET", base+"/traffic?period=current", nil, 200)
	if short["total"] != float64(0) || period["from"] != "2026-09-08" || period["total"] != float64(112) || len(period["points"].([]any)) != 22 {
		t.Fatal("current period did not include measured days outside selected range", short, period)
	}
	edit := map[string]any{"name": "fixture", "quota_bytes": 200, "quota_reset_day": 8, "quota_calibrated_usage_bytes": 150}
	view := c.do("PUT", base, edit, 200)
	usage := view["usage"].(map[string]any)
	if usage["total"] != float64(112) || usage["billed"] != float64(150) || usage["quota_calibration_observed_bytes"] != float64(112) || usage["next_reset"] != "2026-10-08T00:00:00Z" {
		t.Fatal("calibration doubled historical observations", usage)
	}
	delete(edit, "quota_calibrated_usage_bytes")
	view = c.do("PUT", base, edit, 200)
	if view["usage"].(map[string]any)["billed"] != float64(150) {
		t.Fatal("ordinary edit replaced calibration", view["usage"])
	}
	edit["quota_calibrated_usage_bytes"] = 0
	view = c.do("PUT", base, edit, 200)
	usage = view["usage"].(map[string]any)
	if usage["billed"] != float64(0) || usage["quota_calibrated_usage_bytes"] != float64(0) {
		t.Fatal("explicit zero calibration was ignored", usage)
	}
	edit["quota_reset_day"] = 0
	c.do("PUT", base, edit, 400)
	delete(edit, "quota_calibrated_usage_bytes")
	c.do("PUT", base, edit, 200)
	c.do("GET", base+"/traffic?period=current", nil, 400)
	if err := c.api.Store.SetSetting(c.api.ctx(), "quota.action", "stop"); err != nil {
		t.Fatal(err)
	}
	edit["quota_reset_day"] = 8
	edit["quota_bytes"] = 100
	edit["quota_calibrated_usage_bytes"] = 150
	view = c.do("PUT", base, edit, 200)
	if view["enabled"] != false || view["usage"].(map[string]any)["over_quota"] != true {
		t.Fatal("calibrated usage did not enforce the quota", view)
	}
}

func TestServerQuotaBillingAPIAndImmediateEnforcement(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	c.api.Store.Now = func() time.Time { return now }
	c.api.Traffic.Now = c.api.Store.Now
	srv := c.do("POST", "/api/v1/servers", map[string]any{"name": "one-way", "quota_bytes": 200, "quota_reset_day": 8, "quota_billing": "down"}, 201)
	id := int64(srv["id"].(float64))
	base := fmt.Sprintf("/api/v1/servers/%d", id)
	if err := c.api.Store.AddTraffic(c.api.ctx(), store.SubjectServer, id, now, 120, 80); err != nil {
		t.Fatal(err)
	}
	view := c.do("GET", base, nil, 200)
	usage := view["usage"].(map[string]any)
	if view["quota_billing"] != "down" || usage["total"] != float64(200) || usage["billed"] != float64(80) || usage["over_quota"] != false {
		t.Fatal("outbound-only quota did not preserve raw totals", view)
	}
	// Existing clients may omit quota_billing on edit; this must not reset it.
	view = c.do("PUT", base, map[string]any{"name": "one-way", "quota_bytes": 200, "quota_reset_day": 8}, 200)
	if view["quota_billing"] != "down" || view["usage"].(map[string]any)["billed"] != float64(80) {
		t.Fatal("omitted quota mode changed accounting", view)
	}
	c.do("PUT", base, map[string]any{"name": "one-way", "quota_bytes": 200, "quota_reset_day": 8, "quota_billing": "both"}, 400)
	if err := c.api.Store.SetSetting(c.api.ctx(), "quota.action", "stop"); err != nil {
		t.Fatal(err)
	}
	view = c.do("PUT", base, map[string]any{"name": "one-way", "quota_bytes": 100, "quota_reset_day": 8, "quota_billing": "max"}, 200)
	usage = view["usage"].(map[string]any)
	if view["enabled"] != false || view["quota_billing"] != "max" || usage["billed"] != float64(120) || usage["over_quota"] != true {
		t.Fatal("changing quota mode did not enforce overage immediately", view)
	}
}
