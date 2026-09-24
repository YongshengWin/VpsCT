package api

import (
	"context"
	"fmt"
	"testing"

	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
)

func TestSettingsRejectUnverifiedCoreForNetworkBindingsAtomically(t *testing.T) {
	c := newTestAPI(t)
	ctx := context.Background()
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	srv := c.do("POST", "/api/v1/servers", map[string]any{"name": "core-compatibility"}, 201)
	sid := int64(srv["id"].(float64))
	node := c.do("POST", fmt.Sprintf("/api/v1/servers/%d/nodes", sid), map[string]any{"protocol": "ss", "port": 21001}, 201)
	nid := int64(node["id"].(float64))
	policy := &networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block"}
	if _, err := c.api.Store.SetNodeNetwork(ctx, nid, 0, policy, nil); err != nil {
		t.Fatal(err)
	}
	c.do("PUT", "/api/v1/settings", map[string]string{domain.SettingSiteName: "before"}, 200)
	before, err := c.api.Store.LatestDesiredState(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	c.do("PUT", "/api/v1/settings", map[string]string{domain.SettingSiteName: "after", domain.SettingSingBoxVersion: "1.15.0"}, 409)
	if got := c.api.Store.GetSetting(ctx, domain.SettingSiteName, ""); got != "before" {
		t.Fatal("rejected settings partially saved")
	}
	after, err := c.api.Store.LatestDesiredState(ctx, sid)
	if err != nil || after.Revision != before.Revision {
		t.Fatal("rejected settings published new desired", err)
	}
	c.do("PUT", "/api/v1/settings", map[string]string{domain.SettingSingBoxVersion: domain.DefaultSingBoxVersion}, 200)
	c.do("PUT", "/api/v1/settings", map[string]string{domain.SettingSiteName: "also rejected", "invalid.key": "x"}, 400)
	if got := c.api.Store.GetSetting(ctx, domain.SettingSiteName, ""); got != "before" {
		t.Fatal("invalid key caused a partial form save")
	}
}
