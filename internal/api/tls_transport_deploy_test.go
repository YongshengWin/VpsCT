package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/auth"
	"ctlvps/internal/domain"
)

func TestTLSTransportDeploymentBearer(t *testing.T) {
	c, id := natAPIClient(t)
	base := fmt.Sprintf("/api/v1/servers/%d", id)
	key, _ := issueTestAPIKey(t, c, 1, "read", "write")
	read, _ := issueTestAPIKey(t, c, 1, "read")
	member := c.do("POST", "/api/v1/users", map[string]any{"username": "member", "password": "local-fixture-password", "role": "user", "enabled": true}, 201)
	memberKey, _ := issueTestMemberSession(t, c, int64(member["id"].(float64)))
	token := auth.NewSubscriptionToken()
	sub := domain.Subscription{Name: "profile-fixture", Kind: "generated", Enabled: true, Token: token, TokenHash: auth.HashToken(token), ShortCode: "tls-profiles", NodeSelection: domain.NodeSelection{IncludeAll: true}}
	if err := c.api.Store.CreateSubscription(t.Context(), &sub); err != nil {
		t.Fatal(err)
	}
	fetchSubscription := func(want int) []byte {
		t.Helper()
		resp, err := http.Get(c.srv.URL + "/r/tls-profiles?format=singbox")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("public subscription: got %d want %d", resp.StatusCode, want)
		}
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	var applications []agentproto.NodeApplication
	port := 41010
	for _, protocol := range []string{"vless", "trojan"} {
		for _, transport := range []string{"ws", "grpc", "tcp"} {
			if protocol == "trojan" && transport == "tcp" {
				continue
			}
			port++
			combo := map[string]any{"transport": transport, "security": "tls"}
			switch transport {
			case "ws":
				combo["ws"] = map[string]string{"host": "edge.example.org", "path": "/echo"}
			case "grpc":
				combo["grpc"] = map[string]string{"service_name": "echo.Service"}
			}
			body := map[string]any{"protocol": protocol, protocol: combo, "domain": "origin.example.org", "cert_mode": "external", "cert_id": "fixture", "port": port}
			if port == 41011 {
				v, _ := keyRequest(t, c, "GET", base, key, nil, nil, 200)
				if reason, ok := v["tls_transport_block_reason"].(string); !ok || reason == "" {
					t.Fatal("missing reason for legacy agent")
				}
				for _, denied := range []string{read, memberKey} {
					keyRequest(t, c, "POST", base+"/nodes", denied, body, nil, 403)
				}
				r, _ := keyRequest(t, c, "POST", base+"/nodes", key, body, nil, 409)
				if r["error"].(map[string]any)["code"] != "tls_transport_unavailable" {
					t.Fatal("missing capability error")
				}
				c.do("POST", "/api/agent/v1/heartbeat", agentproto.Heartbeat{Version: "test", Metrics: agentproto.Metrics{Arch: "amd64"}, Diagnostics: agentproto.Diagnostics{SecurityVersion: 1, SecurityPolicy: true, Nftables: true, TLSTransportVersion: 1}}, 200)
				v, _ = keyRequest(t, c, "GET", base, key, nil, nil, 200)
				if v["tls_transport_block_reason"] != "" {
					t.Fatal("host unexpectedly blocked")
				}
			}
			n, _ := keyRequest(t, c, "POST", base+"/nodes", key, body, nil, 201)
			stored, err := c.api.Store.GetNode(t.Context(), int64(n["id"].(float64)))
			if err != nil {
				t.Fatal(err)
			}
			applications = append(applications, agentproto.NodeApplication{NodeID: stored.ID, Core: "singbox", Status: "applied", TLSTransportVersion: 1})
			if _, ready, _ := c.api.Store.AppliedSubscriptionNode(t.Context(), stored); ready {
				t.Fatal("node available before apply")
			}
			params := n["params"].(map[string]any)
			if params["flow"] != nil || params["reality-opts"] != nil || (params["servername"] != "origin.example.org" && params["sni"] != "origin.example.org") {
				t.Fatalf("%s %s TLS lost", protocol, transport)
			}
			if transport == "ws" {
				ws := params["ws-opts"].(map[string]any)
				if ws["path"] != "/echo" || ws["headers"].(map[string]any)["Host"] != "edge.example.org" {
					t.Fatal("WS parameters changed")
				}
			}
			if transport == "grpc" && params["grpc-opts"].(map[string]any)["grpc-service-name"] != "echo.Service" {
				t.Fatal("gRPC service changed")
			}
		}
	}
	// The public URL must follow the applied revision for every node.
	fetchSubscription(422)
	rec, err := c.api.Store.LatestDesiredState(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	report := agentproto.ApplyReport{Revision: rec.Revision, Hash: rec.Hash, Status: "applied", NodeApplicationSequence: 1, NodeApplications: applications}
	keyRequest(t, c, "POST", agentproto.PathApplyReport, c.agent, report, map[string]string{agentproto.TLSTransportHeader: "1"}, 204)
	var profile struct {
		Outbounds []struct {
			Type string
			TLS  struct {
				ServerName string `json:"server_name"`
			}
			Transport struct {
				Type, Path  string
				ServiceName string `json:"service_name"`
				Headers     map[string]string
			}
		}
	}
	if err := json.Unmarshal(fetchSubscription(200), &profile); err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, out := range profile.Outbounds {
		if out.Type != "vless" && out.Type != "trojan" {
			continue
		}
		if out.TLS.ServerName != "origin.example.org" {
			t.Fatal("public subscription changed TLS identity")
		}
		if out.Transport.Type == "ws" && (out.Transport.Path != "/echo" || out.Transport.Headers["Host"] != "edge.example.org") {
			t.Fatal("public subscription lost WebSocket identity")
		}
		if out.Transport.Type == "grpc" && out.Transport.ServiceName != "echo.Service" {
			t.Fatal("public subscription lost gRPC service")
		}
		seen[out.Type+"/"+out.Transport.Type]++
	}
	for _, combo := range []string{"vless/", "vless/ws", "vless/grpc", "trojan/ws", "trojan/grpc"} {
		if seen[combo] != 1 {
			t.Fatalf("public subscription contains %d entries for %s", seen[combo], combo)
		}
	}
}

func TestTLSTransportOpenAPI(t *testing.T) {
	c := setupAPIKeyTest(t)
	key, _ := issueTestAPIKey(t, c, 1, "read", "write")
	doc, _ := keyRequest(t, c, "GET", "/api/v1/agent/openapi.json", key, nil, nil, 200)
	paths := doc["paths"].(map[string]any)
	for _, route := range []string{"/api/v1/servers/{id}/nodes", "/api/v1/shares"} {
		operation := paths[route].(map[string]any)["post"].(map[string]any)
		body, err := json.Marshal(operation["requestBody"])
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"trojan", "vless", "ws", "path", "host", "grpc", "service_name", "cert_mode", "cert_id"} {
			if !bytes.Contains(body, []byte(`"`+field+`"`)) {
				t.Fatalf("%s missing %s in request schema", route, field)
			}
		}
	}
	operation := paths["/api/v1/servers/{id}"].(map[string]any)["get"].(map[string]any)
	response, err := json.Marshal(operation["responses"])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(response, []byte(`"tls_transport_block_reason"`)) {
		t.Fatal("missing server capability in response schema")
	}
}
