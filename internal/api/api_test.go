package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/connlog"
	"ctlvps/internal/desired"
	"ctlvps/internal/share"
	"ctlvps/internal/store"
	"ctlvps/internal/subscription"
	"ctlvps/internal/traffic"
)

const testSetupToken = "test-only-setup-capability-not-a-real-secret"

type client struct {
	api    *API
	t      *testing.T
	srv    *httptest.Server
	cookie *http.Cookie
	agent  string
}

func newTestAPI(t *testing.T) *client {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cl, err := connlog.Open(filepath.Join(dir, "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cl.Close() })
	subs := subscription.NewService(st)
	if err := subs.Seed(context.Background()); err != nil {
		t.Fatal(err)
	}
	des := desired.New(st)
	a := New(Deps{Store: st, Connlog: cl, Subs: subs, Desired: des, Shares: share.New(st, des), Traffic: traffic.New(st),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Config: Config{SetupToken: testSetupToken, DataDir: dir, Version: "test", StartedAt: time.Now()}})
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)
	return &client{t: t, srv: srv, api: a}
}

func (c *client) do(method, path string, body any, want int) map[string]any {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", c.srv.URL)
	if c.cookie != nil {
		req.AddCookie(c.cookie)
		req.Header.Set("X-CSRF-Token", c.api.csrfToken(c.cookie.Value))
	}
	if c.agent != "" && strings.HasPrefix(path, "/api/agent/") {
		req.Header.Set("Authorization", "Bearer "+c.agent)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		c.t.Fatalf("%s %s: want %d got %d: %s", method, path, want, resp.StatusCode, raw)
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == sessionCookie && ck.Value != "" {
			c.cookie = ck
		}
	}
	var out any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	switch v := out.(type) {
	case map[string]any:
		return v
	case []any:
		return map[string]any{"list": v}
	}
	return map[string]any{}
}

func TestEndToEnd(t *testing.T) {
	c := newTestAPI(t)
	// setup + auth
	st := c.do("GET", "/api/v1/auth/setup", nil, 200)
	if st["needs_setup"] != true {
		t.Fatal("expected needs_setup")
	}
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	me := c.do("GET", "/api/v1/auth/me", nil, 200)
	if me["role"] != "admin" {
		t.Fatalf("me: %v", me)
	}
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "x", "password": "password123"}, 409)

	// server + enroll
	srv := c.do("POST", "/api/v1/servers", map[string]any{"name": "hk-1", "region": "HK", "quota_bytes": 1 << 30, "quota_reset_day": 1}, 201)
	sid := int64(srv["id"].(float64))
	if srv["agent_status"] != "pending" {
		t.Fatalf("agent status: %v", srv["agent_status"])
	}
	et := c.do("POST", fmt.Sprintf("/api/v1/servers/%d/enroll-token", sid), nil, 200)
	if !strings.Contains(et["install_command"].(string), "install-agent.sh") {
		t.Fatal("install command")
	}
	saved := c.cookie
	c.cookie = nil
	en := c.do("POST", "/api/agent/v1/enroll", agentproto.EnrollRequest{EnrollToken: et["token"].(string), Version: "test", Arch: "amd64"}, 200)
	c.agent = en["agent_token"].(string)
	c.do("POST", "/api/agent/v1/enroll", agentproto.EnrollRequest{EnrollToken: et["token"].(string)}, 401) // one-time
	c.cookie = saved

	// deploy a node on the server, then a share with 2 protocols
	node := c.do("POST", fmt.Sprintf("/api/v1/servers/%d/nodes", sid), map[string]any{"protocol": "vless", "name": "hk-reality"}, 201)
	if node["listen_port"].(float64) == 0 {
		t.Fatal("no port")
	}
	sh := c.do("POST", "/api/v1/shares", map[string]any{"name": "alice", "quota_bytes": 1000, "reset_day": 1, "connlog_enabled": true,
		"targets": []map[string]any{{"server_id": sid, "protocols": []string{"hysteria2", "snell"}}}}, 201)
	shID := int64(sh["id"].(float64))
	sub := sh["subscription"].(map[string]any)
	token := sub["token"].(string)
	if len(sh["nodes"].([]any)) != 2 {
		t.Fatalf("share nodes: %v", sh["nodes"])
	}

	// agent pulls desired state: 3 nodes, connlog enabled
	req, _ := http.NewRequest("GET", c.srv.URL+"/api/agent/v1/desired", nil)
	req.Header.Set("Authorization", "Bearer "+c.agent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var ds agentproto.DesiredState
	_ = json.NewDecoder(resp.Body).Decode(&ds)
	resp.Body.Close()
	if len(ds.Nodes) != 3 || !ds.Connlog.Enabled || ds.Revision == 0 {
		t.Fatalf("desired: nodes=%d connlog=%v rev=%d", len(ds.Nodes), ds.Connlog.Enabled, ds.Revision)
	}
	var sharePort int
	var shareNodeID int64
	for _, n := range ds.Nodes {
		if n.ShareID != nil && n.Protocol == "hysteria2" {
			sharePort = n.ListenPort
			shareNodeID = n.NodeID
			if n.Core != "singbox" || n.Cert == nil {
				t.Fatalf("hy2 spec: %+v", n)
			}
		}
		if n.Protocol == "snell" && n.Core != "snell" {
			t.Fatalf("snell must use snell core: %+v", n)
		}
	}

	// heartbeats: baseline then consumption over quota -> share exhausted
	hb := agentproto.Heartbeat{Version: "test", Epoch: "e1", TS: time.Now(), PublicIPv4: "203.0.113.5",
		Metrics: agentproto.Metrics{NetRx: 1000, NetTx: 1000, MemTotal: 1 << 30}, Ports: []agentproto.PortCounter{{Port: sharePort, Rx: 0, Tx: 0}},
		AppliedRevision: ds.Revision, AppliedHash: ds.Hash, ApplyStatus: "applied"}
	c.do("POST", "/api/agent/v1/heartbeat", hb, 200)
	hb.Metrics.NetRx, hb.Metrics.NetTx = 5000, 5000
	hb.Ports[0].Rx, hb.Ports[0].Tx = 700, 700
	hbr := c.do("POST", "/api/agent/v1/heartbeat", hb, 200)
	got := c.do("GET", fmt.Sprintf("/api/v1/shares/%d", shID), nil, 200)
	if got["status"] != "exhausted" {
		t.Fatalf("share should be exhausted: %v", got["status"])
	}
	if hbr["desired_revision"].(float64) <= float64(ds.Revision) {
		t.Fatal("exhaustion must publish a new revision")
	}
	sv := c.do("GET", fmt.Sprintf("/api/v1/servers/%d", sid), nil, 200)
	if sv["agent_status"] != "online" {
		t.Fatalf("agent should be online: %v", sv["agent_status"])
	}
	if sv["usage"].(map[string]any)["billed"].(float64) != 8000 {
		t.Fatalf("server usage: %v", sv["usage"])
	}
	// deployed node inherited the agent IP
	nodes := c.do("GET", fmt.Sprintf("/api/v1/nodes?server_id=%d", sid), nil, 200)["list"].([]any)
	if nodes[0].(map[string]any)["server"] != "203.0.113.5" {
		t.Fatalf("node host: %v", nodes[0].(map[string]any)["server"])
	}

	// connection logs: share + owner (self-use) nodes
	ownerID := int64(node["id"].(float64))
	c.do("POST", "/api/agent/v1/connlog", agentproto.ConnlogBatch{Seq: 1, Events: []agentproto.ConnEvent{
		{TS: time.Now(), NodeID: shareNodeID, Network: "tcp", DestHost: "www.google.com", DestPort: 443, SrcHost: "203.0.113.10"},
		{TS: time.Now(), NodeID: shareNodeID, Network: "tcp", DestHost: "www.google.com", DestPort: 443, SrcHost: "203.0.113.10"},
		{TS: time.Now(), NodeID: ownerID, Network: "tcp", DestHost: "private.example", DestPort: 443, SrcHost: "198.51.100.2"},
	}}, 200)
	cl := c.do("GET", fmt.Sprintf("/api/v1/connlog?share_id=%d", shID), nil, 200)
	if cl["total"].(float64) != 2 {
		t.Fatalf("connlog share total: %v", cl["total"])
	}
	self := c.do("GET", "/api/v1/connlog?share_id=self", nil, 200)
	if self["total"].(float64) != 1 {
		t.Fatalf("connlog self total: %v", self["total"])
	}
	top := c.do("GET", fmt.Sprintf("/api/v1/connlog/top?share_id=%d", shID), nil, 200)["list"].([]any)
	if len(top) != 1 || top[0].(map[string]any)["hits"].(float64) != 2 {
		t.Fatalf("top domains: %v", top)
	}
	sum := c.do("GET", "/api/v1/connlog/summary", nil, 200)
	clients := sum["clients"].([]any)
	if len(clients) != 2 {
		t.Fatalf("summary clients: %v", sum["clients"])
	}

	// public subscription: exhausted share still renders (soft) with userinfo
	resp, err = http.Get(c.srv.URL + "/s/" + token + "/mihomo")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "proxies:") {
		t.Fatalf("public sub: %d %s", resp.StatusCode, body)
	}
	if ui := resp.Header.Get("Subscription-Userinfo"); !strings.Contains(ui, "total=1000") {
		t.Fatalf("userinfo: %q", ui)
	}
	// UA detection
	req, _ = http.NewRequest("GET", c.srv.URL+"/s/"+token, nil)
	req.Header.Set("User-Agent", "Surge iOS/3000")
	resp, _ = http.DefaultClient.Do(req)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(body), "proxies:") || strings.Contains(string(body), "mixed-port:") {
		t.Fatalf("surge detection failed: %s", body[:min(len(body), 200)])
	}
	// revoke -> 410
	c.do("POST", fmt.Sprintf("/api/v1/shares/%d/revoke", shID), nil, 200)
	resp, _ = http.Get(c.srv.URL + "/s/" + token)
	resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("revoked share should be 410, got %d", resp.StatusCode)
	}
	// unknown token 404, ban rule 403
	resp, _ = http.Get(c.srv.URL + "/s/nope")
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatal("unknown token")
	}
	c.do("POST", "/api/v1/bans", map[string]any{"kind": "ua", "value": "badbot"}, 201)
	req, _ = http.NewRequest("GET", c.srv.URL+"/s/"+token, nil)
	req.Header.Set("User-Agent", "badbot/1.0")
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("ban: %d", resp.StatusCode)
	}

	// generated subscription with import + preview
	imp := c.do("POST", "/api/v1/nodes/import", map[string]any{"text": "ss://YWVzLTI1Ni1nY206cGFzcw==@1.2.3.4:8388#JP-1\ntrojan://pw@jp.example.com:443?sni=jp.example.com#JP-2"}, 200)
	if len(imp["created"].([]any)) != 2 {
		t.Fatalf("import: %v", imp)
	}
	gen := c.do("POST", "/api/v1/subscriptions", map[string]any{"name": "mine", "kind": "generated",
		"node_selection": map[string]any{"include_all": true},
		"proxy_groups":   []map[string]any{{"name": "PROXY", "type": "select", "include_all": true}},
		"rules":          []string{"MATCH,PROXY"}}, 201)
	if gen["node_count"].(float64) < 3 {
		t.Fatalf("generated node count: %v", gen["node_count"])
	}
	c.do("PUT", fmt.Sprintf("/api/v1/subscriptions/%v", gen["id"]), map[string]any{"traffic_limit_bytes": 1000}, 200)
	renamed := c.do("PUT", fmt.Sprintf("/api/v1/subscriptions/%v", gen["id"]), map[string]any{"name": "renamed-mine"}, 200)
	if renamed["name"] != "renamed-mine" {
		t.Fatalf("rename: %v", renamed["name"])
	}
	if renamed["traffic_limit_bytes"].(float64) != 1000 {
		t.Fatalf("rename must not wipe traffic limit: %v", renamed["traffic_limit_bytes"])
	}
	cycled := c.do("PUT", fmt.Sprintf("/api/v1/subscriptions/%v", gen["id"]), map[string]any{"reset_day": 1}, 200)
	if cycled["reset_day"].(float64) != 1 || cycled["next_reset"] == nil {
		t.Fatalf("reset_day: %v next_reset=%v", cycled["reset_day"], cycled["next_reset"])
	}
	req, _ = http.NewRequest("GET", c.srv.URL+"/s/"+cycled["token"].(string), nil)
	resp, _ = http.DefaultClient.Do(req)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Subscription-Userinfo"), "expire=") {
		t.Fatalf("cycled sub userinfo: %d %q %s", resp.StatusCode, resp.Header.Get("Subscription-Userinfo"), body[:min(len(body), 120)])
	}
	links := gen["links"].(map[string]any)
	if !strings.Contains(links["singbox"].(string), "/s/") {
		t.Fatal("links")
	}
	rendered := c.do("GET", fmt.Sprintf("/api/v1/subscriptions/%v/render?format=singbox", gen["id"]), nil, 200)
	if !strings.Contains(rendered["body"].(string), `"outbounds"`) {
		t.Fatal("singbox render")
	}
	// normal user cannot see admin resources
	c.do("POST", "/api/v1/users", map[string]any{"username": "bob", "password": "password123", "role": "user"}, 201)
	c.do("POST", "/api/v1/auth/logout", nil, 204)
	c.cookie = nil
	c.do("GET", "/api/v1/servers", nil, 401)
	c.do("POST", "/api/v1/auth/login", map[string]any{"username": "bob", "password": "password123"}, 200)
	c.do("GET", "/api/v1/servers", nil, 403)
	if l := c.do("GET", "/api/v1/subscriptions", nil, 200)["list"].([]any); len(l) != 0 {
		t.Fatalf("bob should see no subscriptions, got %d", len(l))
	}
	dash := c.do("GET", "/api/v1/dashboard", nil, 200)
	if _, ok := dash["counts"]; ok {
		t.Fatal("user dashboard must not include admin counts")
	}
}
