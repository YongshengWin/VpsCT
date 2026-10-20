package api

import (
	"fmt"
	"testing"
	"time"

	"ctlvps/internal/store"
)

func TestServerDirectionalCalibrationUsesPeriodTotalsBeforeMaximum(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	c.api.Store.Now = func() time.Time { return now }
	c.api.Traffic.Now = c.api.Store.Now
	v := c.do("POST", "/api/v1/servers", map[string]any{"name": "directional", "quota_reset_day": 8, "quota_billing": "max"}, 201)
	id := int64(v["id"].(float64))
	path := fmt.Sprintf("/api/v1/servers/%d", id)
	if err := c.api.Store.AddTraffic(c.api.ctx(), store.SubjectServer, id, now, 100, 50); err != nil {
		t.Fatal(err)
	}
	edit := map[string]any{"name": "directional", "quota_reset_day": 8, "quota_billing": "max", "quota_bytes": 270,
		"quota_calibrated_inbound_bytes": 200, "quota_calibrated_outbound_bytes": 180}
	v = c.do("PUT", path, edit, 200)
	usage := v["usage"].(map[string]any)
	if usage["billed"] != float64(200) || usage["billed_estimated"] != false || usage["total"] != float64(150) {
		t.Fatal("directional calibration changed raw observations or lost its basis", usage)
	}
	if err := c.api.Store.AddTraffic(c.api.ctx(), store.SubjectServer, id, now, 10, 80); err != nil {
		t.Fatal(err)
	}
	v = c.do("GET", path, nil, 200)
	usage = v["usage"].(map[string]any)
	// max(200+10,180+80)=260; max(200,180)+max(10,80)=280
	// would incorrectly disable this server at its 270-byte quota.
	if usage["billed"] != float64(260) || usage["over_quota"] != false || usage["total"] != float64(240) || usage["billed_estimated"] != false {
		t.Fatal("calibration summed per-sample maxima", usage)
	}
	delete(edit, "quota_calibrated_inbound_bytes")
	delete(edit, "quota_calibrated_outbound_bytes")
	v = c.do("PUT", path, edit, 200)
	if v["usage"].(map[string]any)["billed"] != float64(260) {
		t.Fatal("ordinary edit discarded provider directions", v["usage"])
	}
	edit["quota_calibrated_usage_bytes"] = 200
	v = c.do("PUT", path, edit, 200)
	if v["usage"].(map[string]any)["billed_estimated"] != true {
		t.Fatal("scalar maximum calibration was presented as exact", v["usage"])
	}
	edit["quota_calibrated_inbound_bytes"], edit["quota_calibrated_outbound_bytes"] = 200, 180
	edit["quota_calibrated_usage_bytes"] = 201
	c.do("PUT", path, edit, 400)
	delete(edit, "quota_calibrated_usage_bytes")
	delete(edit, "quota_calibrated_outbound_bytes")
	c.do("PUT", path, edit, 400)
	edit["quota_calibrated_outbound_bytes"] = -1
	c.do("PUT", path, edit, 400)
	edit["quota_calibrated_outbound_bytes"] = 180
	edit["quota_reset_day"] = 0
	c.do("PUT", path, edit, 400)
}

func TestNewServerDirectionalCalibrationAndZero(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	c.do("POST", "/api/v1/servers", map[string]any{"name": "invalid-quota", "quota_bytes": -1}, 400)
	v := c.do("POST", "/api/v1/servers", map[string]any{"name": "zero-directional", "quota_reset_day": 1, "quota_billing": "max",
		"quota_calibrated_inbound_bytes": 0, "quota_calibrated_outbound_bytes": 0}, 201)
	u := v["usage"].(map[string]any)
	if u["billed"] != float64(0) || u["quota_calibrated_inbound_bytes"] != float64(0) || u["quota_calibrated_outbound_bytes"] != float64(0) || u["billed_estimated"] != false {
		t.Fatal("explicit zero directional snapshot was omitted", u)
	}
}
