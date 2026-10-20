package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/desired"
	"ctlvps/internal/natconfig"
)

func natAPIClient(t *testing.T) (*client, int64) {
	t.Helper()
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	srv := c.do("POST", "/api/v1/servers", map[string]any{"name": "NAT fixture", "public_host": "192.0.2.10"}, 201)
	id := int64(srv["id"].(float64))
	et := c.do("POST", fmt.Sprintf("/api/v1/servers/%d/enroll-token", id), nil, 200)
	cookie := c.cookie
	c.cookie = nil
	en := c.do("POST", "/api/agent/v1/enroll", agentproto.EnrollRequest{EnrollToken: et["token"].(string), Version: "test", Arch: "amd64"}, 200)
	c.agent = en["agent_token"].(string)
	c.cookie = cookie
	return c, id
}
func natAPIReport(t *testing.T, c *client, n *agentproto.NATStatus, want int) {
	t.Helper()
	c.do("POST", "/api/agent/v1/heartbeat", agentproto.Heartbeat{Version: "test", Diagnostics: agentproto.Diagnostics{SecurityVersion: 1, SecurityPolicy: true, NetworkConfigureAllowed: true, NAT: n}}, want)
}
func natAPIConfig() *natconfig.Config {
	return &natconfig.Config{Schema: 1, Mappings: []natconfig.Mapping{{PublicPort: 30001, LocalPort: 10001, Count: 2, Network: "both"}}}
}

func TestNATAPIStrictCASPendingAndDesiredClear(t *testing.T) {
	c, id := natAPIClient(t)
	base := fmt.Sprintf("/api/v1/servers/%d/nat", id)
	v := c.do("GET", base, nil, 200)
	if v["editable"] != false || v["revision"] != float64(0) {
		t.Fatal(v)
	}
	c.do("PUT", base, map[string]any{"expected_revision": 0, "config": natAPIConfig()}, 409)
	natAPIReport(t, c, &agentproto.NATStatus{Version: 2, Source: "none", Status: "disabled"}, 200)
	v = c.do("PUT", base, map[string]any{"expected_revision": 0, "config": natAPIConfig()}, 200)
	if v["pending"] != true || v["revision"] != float64(1) {
		t.Fatal(v)
	}
	c.do("PUT", base, map[string]any{"expected_revision": 0, "config": nil}, 409)
	v = c.do("PUT", base, map[string]any{"expected_revision": 1, "config": natAPIConfig()}, 200)
	if v["revision"] != float64(1) {
		t.Fatal("no-op bumped revision")
	}
	// Validate both body layers without permissive unknown/duplicate fields.
	for _, raw := range []string{
		`{"expected_revision":1,"config":null,"extra":true}`,
		`{"expected_revision":1,"expected_revision":1,"config":null}`,
		`{"expected_revision":1,"config":{"schema":1,"mappings":[],"extra":true}}`,
		`{"expected_revision":1,"config":{"schema":1,"schema":1,"mappings":[]}}`,
		`{"expected_revision":null,"config":null}`,
		`{"expected_revision":1,"config":{"schema":1,"mappings":[{"public_port":65535,"local_port":10001,"count":2}]}}`,
		`{"expected_revision":1,"config":null} {}`,
	} {
		req, _ := http.NewRequest("PUT", c.srv.URL+base, strings.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", c.srv.URL)
		req.Header.Set("X-CSRF-Token", c.api.csrfToken(c.cookie.Value))
		req.AddCookie(c.cookie)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Fatal("strict JSON", resp.StatusCode)
		}
	}
	c.do("POST", fmt.Sprintf("/api/v1/servers/%d/nodes", id), map[string]any{"protocol": "vless"}, 409)
	// Stale error belongs to the old config, and must not overshadow new intent.
	old := natAPIConfig()
	old.Mappings[0].PublicPort = 30010
	natAPIReport(t, c, &agentproto.NATStatus{Version: 2, Source: "controller", Configured: true, Status: "error", Config: old, Error: "old configuration failure"}, 200)
	server := c.do("GET", fmt.Sprintf("/api/v1/servers/%d", id), nil, 200)
	summary := server["nat"].(map[string]any)
	if summary["status"] != "pending" || summary["error"] != nil {
		t.Fatal(summary)
	}
	natAPIReport(t, c, &agentproto.NATStatus{Version: 2, Source: "controller", Configured: true, Status: "error", Config: natAPIConfig(), Error: "current configuration failure"}, 200)
	v = c.do("GET", base, nil, 200)
	if v["pending"] != false {
		t.Fatal("current error hidden by pending")
	}
	natAPIReport(t, c, &agentproto.NATStatus{Version: 2, Source: "controller", Configured: true, Status: "applied", Config: natAPIConfig()}, 200)
	listWire, _ := json.Marshal(c.do("GET", "/api/v1/servers", nil, 200))
	if bytes.Contains(listWire, []byte("mappings")) {
		t.Fatal("applied NAT list exposed full mapping")
	}
	v = c.do("PUT", base, map[string]any{"expected_revision": 1, "config": nil}, 200)
	if v["revision"] != float64(2) || v["pending"] != true {
		t.Fatal(v)
	}
	rec, err := c.api.Store.LatestDesiredState(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	ds, err := desired.Load(rec)
	if err != nil || ds.NATVersion != 2 || ds.NAT != nil {
		t.Fatal("clear lost NAT wire contract", err)
	}
}

func TestNATAPIHeartbeatRejectsInvalidReportAndLocalReadOnly(t *testing.T) {
	c, id := natAPIClient(t)
	base := fmt.Sprintf("/api/v1/servers/%d/nat", id)
	for _, n := range []*agentproto.NATStatus{{Version: 1, Source: "none", Status: "disabled"}, {Version: 2, Source: "none", Configured: true, Status: "applied", Config: natAPIConfig()}, {Version: 2, Source: "local", Status: "error", Error: strings.Repeat("x", 513)}} {
		natAPIReport(t, c, n, 400)
	}
	v := c.do("GET", base, nil, 200)
	if v["report"] != nil {
		t.Fatal("invalid report persisted")
	}
	natAPIReport(t, c, &agentproto.NATStatus{Version: 2, Source: "local", Status: "error", Error: "invalid local file"}, 200)
	v = c.do("GET", base, nil, 200)
	if v["editable"] != false || v["pending"] != false {
		t.Fatal(v)
	}
	c.do("PUT", base, map[string]any{"expected_revision": 0, "config": natAPIConfig()}, 409)
	natAPIReport(t, c, &agentproto.NATStatus{Version: 2, Source: "local", Status: "disabled", Config: &natconfig.Config{Schema: 1}}, 200)
	c.do("PUT", base, map[string]any{"expected_revision": 0, "config": natAPIConfig()}, 409)
	b, _ := json.Marshal(c.do("GET", "/api/v1/servers", nil, 200))
	if bytes.Contains(b, []byte("mappings")) {
		t.Fatal("list exposed full mapping")
	}
}
