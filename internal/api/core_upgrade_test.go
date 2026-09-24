package api

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"ctlvps/internal/agentproto"
)

func TestCoreUpgradeRequiresAppliedRunningOfficialVersion(t *testing.T) {
	ready := agentproto.CoreStatus{Name: "sing-box", Version: "1.14.1", Installed: true, Wanted: true, Active: true}
	for _, tc := range []struct {
		name, pin, want      string
		core                 agentproto.CoreStatus
		online, sync, failed bool
	}{
		{"old pin", "1.12.14", "upgrade", ready, true, true, false},
		{"offline with good last report", "1.14.1", "offline", ready, false, true, false},
		{"not applied", "1.14.1", "pending", ready, true, false, false},
		{"failed application", "1.14.1", "error", ready, true, true, true},
		{"ready", "1.14.1", "ready", ready, true, true, false},
		{"different actual version", "1.14.2", "pending", ready, true, true, false},
		{"unsupported future family", "1.15.0", "upgrade", ready, true, true, false},
		{"uninstalled", "1.14.1", "pending", agentproto.CoreStatus{}, true, true, false},
		{"inactive service", "1.14.1", "error", agentproto.CoreStatus{Version: "1.14.1", Installed: true, Wanted: true}, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := assessCoreUpgrade(tc.pin, tc.core, tc.online, tc.sync, tc.failed)
			if got != tc.want {
				t.Fatalf("status=%s, want %s", got, tc.want)
			}
		})
	}
}

func TestCoreUpgradeNoticeRequiresAdminAndManagedCore(t *testing.T) {
	c := newTestAPI(t)
	c.do("GET", "/api/v1/settings/core-upgrade", nil, 401)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	view := c.do("GET", "/api/v1/settings/core-upgrade", nil, 200)
	if view["needs_attention"] != false || len(view["servers"].([]any)) != 0 {
		t.Fatal("empty installation should not nag", view)
	}
	c.do("POST", "/api/v1/servers", map[string]any{"name": "no-core-yet"}, 201)
	view = c.do("GET", "/api/v1/settings/core-upgrade", nil, 200)
	if view["needs_attention"] != false {
		t.Fatal("enrolled server without sing-box should not nag", view)
	}
}

func TestCoreUpgradeNoticeFollowsActualReceipts(t *testing.T) {
	c := newTestAPI(t)
	ctx := context.Background()
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	srv := c.do("POST", "/api/v1/servers", map[string]any{"name": "upgrade-target"}, 201)
	sid := int64(srv["id"].(float64))
	et := c.do("POST", fmt.Sprintf("/api/v1/servers/%d/enroll-token", sid), nil, 200)
	c.do("POST", "/api/agent/v1/enroll", agentproto.EnrollRequest{EnrollToken: et["token"].(string), Version: "test", Arch: "amd64"}, 200)
	ag, err := c.api.Store.GetAgentByServer(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	heartbeat := func(version string, active bool) {
		t.Helper()
		d, _ := json.Marshal(agentproto.Diagnostics{Cores: []agentproto.CoreStatus{{Name: "sing-box", Version: version, Installed: true, Wanted: true, Active: active}}})
		if err := c.api.Store.Heartbeat(ctx, ag.ID, "test", "", "", nil, d); err != nil {
			t.Fatal(err)
		}
	}
	check := func(status string, attention bool) {
		t.Helper()
		v := c.do("GET", "/api/v1/settings/core-upgrade", nil, 200)
		rows := v["servers"].([]any)
		if len(rows) != 1 || rows[0].(map[string]any)["status"] != status || v["needs_attention"] != attention {
			t.Fatal(v)
		}
	}
	heartbeat("1.12.14", true)
	c.do("PUT", "/api/v1/settings", map[string]string{"core.singbox_version": "1.12.14"}, 200)
	check("upgrade", true)
	c.do("PUT", "/api/v1/settings", map[string]string{"core.singbox_version": "1.14.1"}, 200)
	check("pending", true) // Saving the target alone must not dismiss the notice.
	heartbeat("1.14.1", true)
	check("pending", true) // A version report alone is not an apply receipt.
	ds, err := c.api.Store.LatestDesiredState(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.api.Store.SetAgentApplied(ctx, ag.ID, ds.Revision, ds.Hash, ""); err != nil {
		t.Fatal(err)
	}
	check("ready", false)
	heartbeat("1.14.1", false)
	check("error", true)
	server, err := c.api.Store.GetServer(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	server.Enabled = false
	if err = c.api.Store.UpdateServer(ctx, &server); err != nil {
		t.Fatal(err)
	}
	v := c.do("GET", "/api/v1/settings/core-upgrade", nil, 200)
	if len(v["servers"].([]any)) != 0 || v["needs_attention"] != false {
		t.Fatal("disabled server affected upgrade notice", v)
	}
}
