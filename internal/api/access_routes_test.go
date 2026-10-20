package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/store"
)

func accessRouteAPIFixture(t *testing.T) (*client, store.AccessRouteRequest, map[int64]string) {
	t.Helper()
	c := newTestAPI(t)
	ctx := context.Background()
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	ids := []int64{}
	tokens := map[int64]string{}
	names := []string{"测试国内入口", "测试 IX 中转", "测试国际落地"}
	for i := 0; i < 3; i++ {
		v := c.do("POST", "/api/v1/servers", map[string]any{"name": names[i], "public_host": fmt.Sprintf("203.0.113.%d", 10+i)}, 201)
		sid := int64(v["id"].(float64))
		ids = append(ids, sid)
		et := c.do("POST", fmt.Sprintf("/api/v1/servers/%d/enroll-token", sid), nil, 200)
		en := c.do("POST", agentproto.PathEnroll, agentproto.EnrollRequest{EnrollToken: et["token"].(string)}, 200)
		tokens[sid] = en["agent_token"].(string)
		c.agent = tokens[sid]
		hb := agentproto.Heartbeat{
			TS: time.Now().UTC(), Epoch: "fixture",
			Diagnostics: agentproto.Diagnostics{
				NetworkBindingVersion: 1, NetworkForwardVersion: 1, ForwardPrivateVersion: 1, NetworkConfigureAllowed: true,
				SecurityVersion: 1, SecurityPolicy: true, Nftables: true, Systemd: true,
				Cores: []agentproto.CoreStatus{{Name: "sing-box", Version: domain.DefaultSingBoxVersion, Installed: true}},
			},
			Metrics: agentproto.Metrics{
				Arch: "arm64",
				Network: &agentproto.NetworkSnapshot{
					Version: 1, CollectorID: strings.Repeat("b", 32), BootID: "fixture", Sequence: 1, SampledAt: time.Now().UTC(), Status: "ok",
					Interfaces: []agentproto.NetworkInterface{{
						ID: strings.Repeat("1", 32), Generation: strings.Repeat("c", 32), Name: "wan0", Index: 2, Kind: "physical", Up: true, Carrier: true,
						Addresses: []string{fmt.Sprintf("192.0.2.%d/24", 10+i)}, UsableAddresses: []string{fmt.Sprintf("192.0.2.%d/24", 10+i)},
					}},
				},
			},
		}
		c.do("POST", agentproto.PathHeartbeat, hb, 200)
	}
	n := c.do("POST", fmt.Sprintf("/api/v1/servers/%d/nodes", ids[2]), map[string]any{"name": "测试落地 SS", "protocol": "ss", "port": 21001}, 201)
	nid := int64(n["id"].(float64))
	rec, _, err := c.api.Desired.Publish(ctx, ids[2])
	if err != nil {
		t.Fatal(err)
	}
	c.agent = tokens[ids[2]]
	c.do("POST", agentproto.PathApplyReport, agentproto.ApplyReport{Revision: rec.Revision, Hash: rec.Hash, Status: "applied"}, 204)
	c.agent = ""
	return c, store.AccessRouteRequest{ID: strings.Repeat("d", 32), Name: "测试专线", EntryServerID: ids[0], IXServerID: ids[1], LandingNodeID: nid, Network: "tcp", SourceMode: "all", SourceCIDRs: []string{}}, tokens
}
func acknowledgeRouteAPI(t *testing.T, c *client, tokens map[int64]string) {
	t.Helper()
	ctx := context.Background()
	if err := c.api.Desired.ReconcileNetworkOperations(ctx); err != nil {
		t.Fatal(err)
	}
	for sid, token := range tokens {
		rec, err := c.api.Store.LatestDesiredState(ctx, sid)
		if err != nil {
			continue
		}
		c.agent = token
		c.do("POST", agentproto.PathApplyReport, agentproto.ApplyReport{Revision: rec.Revision, Hash: rec.Hash, Status: "applied"}, 204)
	}
	c.agent = ""
}
func TestAccessRouteAPIProvisionsAndExportsIndependentSubscription(t *testing.T) {
	c, in, tokens := accessRouteAPIFixture(t)
	ctx := context.Background()
	before, _ := c.api.Store.GetNode(ctx, in.LandingNodeID)
	c.do("POST", "/api/v1/access-routes", in, 428)
	preview := c.do("POST", "/api/v1/access-routes/preview", in, 200)
	if preview["ready"] != true {
		t.Fatal("valid route rejected", preview)
	}
	raw, _ := json.Marshal(preview["candidate"])
	json.Unmarshal(raw, &in)
	in.ExpectedImpact = preview["token"].(string)
	created := c.do("POST", "/api/v1/access-routes", in, 202)
	c.do("POST", "/api/v1/access-routes", in, 202)
	if created["stage"] != "pending_landing" {
		t.Fatal(created)
	}
	c.do("GET", "/api/v1/access-routes/"+in.ID+"/node", nil, 409)
	var r store.AccessRoute
	for i := 0; i < 4; i++ {
		acknowledgeRouteAPI(t, c, tokens)
		r, _ = c.api.Store.AccessRoute(ctx, in.ID)
	}
	if r.Stage != "applied" {
		t.Fatal("route failed to resume stages", r.Stage, r.Message)
	}
	response := c.do("GET", "/api/v1/access-routes/"+in.ID+"/node", nil, 200)
	if response["uri"] == "" {
		t.Fatal("missing client link")
	}
	projected := response["node"].(map[string]any)
	if projected["server"] != in.AdvertiseHost || int(projected["port"].(float64)) != in.EntryPort {
		t.Fatal("wrong subscription entry")
	}
	after, _ := c.api.Store.GetNode(ctx, in.LandingNodeID)
	if before.Server != after.Server || before.Port != after.Port || string(before.Params) != string(after.Params) {
		t.Fatal("route rewrote landing")
	}
	sub, err := c.api.Store.GetSubscription(ctx, *r.SubscriptionID)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := c.api.Subs.Build(ctx, sub)
	if err != nil || len(bundle.Proxies) != 1 {
		t.Fatal("subscription lost route", err)
	}
	if bundle.Proxies[0].Server != in.AdvertiseHost || bundle.Proxies[0].Port != in.EntryPort {
		t.Fatal("subscription points at landing")
	}
	// Route-only subscriptions still honor their own quota and count the
	// landing node once even when also selected directly.
	if err = c.api.Store.AddTraffic(ctx, store.SubjectNode, in.LandingNodeID, c.api.Store.Now(), 15, 25); err != nil {
		t.Fatal(err)
	}
	sub.TrafficLimitBytes = 1
	bundle, err = c.api.Subs.Build(ctx, sub)
	if err != nil || len(bundle.Proxies) != 0 {
		t.Fatal("route bypassed subscription quota", err)
	}
	sub.TrafficLimitBytes = 0
	sub.NodeSelection.NodeIDs = []int64{in.LandingNodeID}
	bundle, err = c.api.Subs.Build(ctx, sub)
	if err != nil || bundle.Userinfo == nil || bundle.Userinfo.Upload+bundle.Userinfo.Download != 40 {
		t.Fatal("route double-counted source traffic", err)
	}
	sub.NodeSelection.NodeIDs = nil
	sub.NodeSelection.ExcludeFilter = "测试专线"
	bundle, err = c.api.Subs.Build(ctx, sub)
	if err != nil || len(bundle.Proxies) != 0 {
		t.Fatal("route bypassed name filter", err)
	}
	sub.NodeSelection.ExcludeFilter = ""
	c.do("GET", "/api/v1/access-routes", nil, 200)
	// Every mutation, including retries and stop, requires browser CSRF.
	req, _ := http.NewRequest("DELETE", c.srv.URL+"/api/v1/access-routes/"+in.ID, strings.NewReader(fmt.Sprintf(`{"expected_revision":%d}`, r.Revision)))
	req.AddCookie(c.cookie)
	req.Header.Set("Origin", c.srv.URL)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("stop accepted without CSRF", res.StatusCode)
	}
	c.do("DELETE", "/api/v1/access-routes/"+in.ID, map[string]any{"expected_revision": r.Revision}, 202)
	bundle, err = c.api.Subs.Build(ctx, sub)
	if err != nil || len(bundle.Proxies) != 0 {
		t.Fatal("stopped route remains exported", err)
	}
	if _, err = c.api.Store.GetNode(ctx, in.LandingNodeID); err != nil {
		t.Fatal("source disappeared")
	}
}

func TestImportedVLESSLandingPreservesIdentityAndStopsOnEndpointChange(t *testing.T) {
	c, in, tokens := accessRouteAPIFixture(t)
	ctx := context.Background()
	landing := domain.Node{Name: "external VLESS", Protocol: "vless", Server: "landing.example.test", Port: 443, Source: domain.NodeImported, Enabled: true, Params: json.RawMessage(`{"uuid":"11111111-1111-1111-1111-111111111111","tls":true,"network":"ws","udp":true,"reality-opts":{"public-key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","short-id":"ab"}}`)}
	if err := c.api.Store.CreateNode(ctx, &landing); err != nil {
		t.Fatal(err)
	}
	original := string(landing.Params)
	in.LandingNodeID = landing.ID
	in.LandingHost = "203.0.113.30"
	in.Name = "external route"
	preview := c.do("POST", "/api/v1/access-routes/preview", in, 200)
	if preview["ready"] != true {
		t.Fatal("external landing rejected", preview)
	}
	raw, _ := json.Marshal(preview["candidate"])
	json.Unmarshal(raw, &in)
	in.ExpectedImpact = preview["token"].(string)
	c.do("POST", "/api/v1/access-routes", in, 202)
	for i := 0; i < 7; i++ {
		acknowledgeRouteAPI(t, c, tokens)
	}
	r, err := c.api.Store.AccessRoute(ctx, in.ID)
	if err != nil || r.Stage != "applied" {
		t.Fatal("external route required landing receipt", err, r.Stage)
	}
	response := c.do("GET", "/api/v1/access-routes/"+in.ID+"/node", nil, 200)
	raw, _ = json.Marshal(response["node"])
	var projection domain.Node
	json.Unmarshal(raw, &projection)
	var params map[string]any
	json.Unmarshal(projection.Params, &params)
	if projection.Protocol != "vless" || projection.Server != in.AdvertiseHost || projection.Port != in.EntryPort || params["servername"] != landing.Server || params["udp"] != true {
		t.Fatal("changed protocol, TLS identity or tunneled UDP", projection.Protocol, params)
	}
	headers := params["ws-opts"].(map[string]any)["headers"].(map[string]any)
	if headers["Host"] != landing.Server || params["reality-opts"].(map[string]any)["short-id"] != "ab" {
		t.Fatal("lost original HTTP or Reality identity")
	}
	before, err := c.api.Store.GetNode(ctx, landing.ID)
	if err != nil || string(before.Params) != original || before.Server != landing.Server {
		t.Fatal("mutated imported source", err)
	}
	sub, err := c.api.Store.GetSubscription(ctx, *r.SubscriptionID)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := c.api.Subs.Build(ctx, sub)
	if err != nil || len(bundle.Proxies) != 1 || bundle.Proxies[0].Str("servername") != landing.Server {
		t.Fatal("generated subscription lost external route", err)
	}
	landing.Server = "changed.example.test"
	if err := c.api.Store.UpdateNode(ctx, &landing); err != nil {
		t.Fatal(err)
	}
	c.do("GET", "/api/v1/access-routes/"+in.ID+"/node", nil, 409)
	bundle, err = c.api.Subs.Build(ctx, sub)
	if err != nil || len(bundle.Proxies) != 0 {
		t.Fatal("endpoint change left stale export", err)
	}
	if err := c.api.Store.ReconcileAccessRoute(ctx, in.ID); err != nil {
		t.Fatal(err)
	}
	r, _ = c.api.Store.AccessRoute(ctx, in.ID)
	if r.Stage != "blocked" {
		t.Fatal("entry not blocked", r.Stage)
	}
}

func TestDirectAccessRouteAPIExportsAndStopsWithoutRelay(t *testing.T) {
	c, in, tokens := accessRouteAPIFixture(t)
	ctx := context.Background()
	relayID := in.IXServerID
	in.IXServerID = 0
	preview := c.do("POST", "/api/v1/access-routes/preview", in, 200)
	if preview["ready"] != true {
		t.Fatal("direct preview rejected")
	}
	raw, _ := json.Marshal(preview["candidate"])
	json.Unmarshal(raw, &in)
	in.ExpectedImpact = preview["token"].(string)
	created := c.do("POST", "/api/v1/access-routes", in, 202)
	if created["ix_forward_id"] != float64(0) {
		t.Fatal("created extra relay")
	}
	// No relay reports are supplied: only entry and landing are relevant.
	delete(tokens, relayID)
	for i := 0; i < 4; i++ {
		acknowledgeRouteAPI(t, c, tokens)
	}
	r, err := c.api.Store.AccessRoute(ctx, in.ID)
	if err != nil || r.Stage != "applied" {
		t.Fatal(r.Stage, err)
	}
	response := c.do("GET", "/api/v1/access-routes/"+in.ID+"/node", nil, 200)
	node := response["node"].(map[string]any)
	if node["server"] != in.AdvertiseHost || int(node["port"].(float64)) != in.EntryPort {
		t.Fatal("wrong entry endpoint")
	}
	sub, err := c.api.Store.GetSubscription(ctx, *r.SubscriptionID)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := c.api.Subs.Build(ctx, sub)
	if err != nil || len(bundle.Proxies) != 1 {
		t.Fatal("missing direct subscription", err)
	}
	c.do("GET", fmt.Sprintf("/api/v1/servers/%d/forwards", in.EntryServerID), nil, 200)
	c.do("DELETE", "/api/v1/access-routes/"+in.ID, map[string]any{"expected_revision": r.Revision}, 202)
	c.do("GET", "/api/v1/access-routes/"+in.ID+"/node", nil, 409)
}
