package api

import (
	"bytes"
	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestAgentTLSTransportNegotiationReceiptsAndCleanup(t *testing.T) {
	c, id := natAPIClient(t)
	ctx := t.Context()
	key, _ := issueTestAPIKey(t, c, 1, "read", "write")
	base := fmt.Sprintf("/api/v1/servers/%d", id)
	call := func(method, path, cap string, body any, want int, out any) {
		t.Helper()
		var buf bytes.Buffer
		if body != nil {
			if err := json.NewEncoder(&buf).Encode(body); err != nil {
				t.Fatal(err)
			}
		}
		req, err := http.NewRequest(method, c.srv.URL+path, &buf)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+c.agent)
		req.Header.Set("Content-Type", "application/json")
		if cap != "" {
			req.Header.Set(agentproto.TLSTransportHeader, cap)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("%s capability=%q: got %d want %d", path, cap, res.StatusCode, want)
		}
		if out != nil {
			if err = json.NewDecoder(res.Body).Decode(out); err != nil {
				t.Fatal(err)
			}
		}
	}
	hb := agentproto.Heartbeat{Version: "fixture", Metrics: agentproto.Metrics{Arch: "amd64"}, Diagnostics: agentproto.Diagnostics{SecurityVersion: 1, SecurityPolicy: true, Nftables: true, TLSTransportVersion: 1}}
	c.do("POST", agentproto.PathHeartbeat, hb, 200)
	body := map[string]any{"protocol": "vless", "vless": map[string]any{"security": "tls", "transport": "ws", "ws": map[string]any{"path": "/edge"}}, "domain": "origin.example.org", "cert_mode": "self_signed", "port": 41042}
	node, _ := keyRequest(t, c, "POST", base+"/nodes", key, body, nil, 201)
	nid := int64(node["id"].(float64))
	n, err := c.api.Store.GetNode(ctx, nid)
	if err != nil {
		t.Fatal(err)
	}
	var d agentproto.DesiredState
	call("GET", agentproto.PathDesired, "1", nil, 200, &d)
	if d.TLSTransportVersion != 1 || len(d.Nodes) != 1 || d.Nodes[0].Cert == nil || d.Nodes[0].Cert.Domain != "origin.example.org" {
		t.Fatal("desired lost transport capability or cert")
	}
	if err := agentproto.ValidateDesired(&d, id, 0, ""); err != nil {
		t.Fatal(err)
	}
	for _, cap := range []string{"", "2"} {
		call("GET", fmt.Sprintf("%s?if_not_revision=%d", agentproto.PathDesired, d.Revision), cap, nil, 409, nil)
	}
	applied := func(want bool) {
		t.Helper()
		if _, ok, err := c.api.Store.AppliedSubscriptionNode(ctx, n); err != nil || ok != want {
			t.Fatalf("subscription available=%v want=%v: %v", ok, want, err)
		}
	}
	rep := agentproto.ApplyReport{Revision: d.Revision, Hash: d.Hash, Status: "applied", NodeApplicationSequence: 1}
	call("POST", agentproto.PathApplyReport, "", rep, 409, nil)
	call("POST", agentproto.PathApplyReport, "1", rep, 400, nil)
	applied(false)
	hb.ApplyStatus, hb.AppliedHash, hb.AppliedRevision = "applied", d.Hash, d.Revision
	hb.Diagnostics.TLSTransportVersion = 0
	c.do("POST", agentproto.PathHeartbeat, hb, 200)
	applied(false)
	rep.NodeApplications = []agentproto.NodeApplication{{Core: "singbox", NodeID: nid, Status: "applied"}}
	call("POST", agentproto.PathApplyReport, "1", rep, 400, nil)
	applied(false)
	rep.Status = "failed"
	rep.NodeApplications[0].Status = "failed"
	call("POST", agentproto.PathApplyReport, "1", rep, 204, nil)
	applied(false)
	rep.NodeApplicationSequence++
	rep.Status = "applied"
	rep.NodeApplications[0].Status = "applied"
	rep.NodeApplications[0].TLSTransportVersion = 1
	call("POST", agentproto.PathApplyReport, "1", rep, 204, nil)
	applied(true)
	keyRequest(t, c, "DELETE", fmt.Sprintf("/api/v1/nodes/%d", nid), key, nil, nil, 204)
	var cleanup agentproto.DesiredState
	call("GET", agentproto.PathDesired, "1", nil, 200, &cleanup)
	if cleanup.TLSTransportVersion != 1 || len(cleanup.Nodes) != 0 || cleanup.Revision <= d.Revision {
		t.Fatal("cleanup lost floor")
	}
	call("GET", agentproto.PathDesired, "", nil, 409, nil)
	clean := agentproto.ApplyReport{Revision: cleanup.Revision, Hash: cleanup.Hash, Status: "applied", NodeApplicationSequence: 3}
	call("POST", agentproto.PathApplyReport, "", clean, 409, nil)
	hb.AppliedRevision, hb.AppliedHash = clean.Revision, clean.Hash
	c.do("POST", agentproto.PathHeartbeat, hb, 200)
	ag, err := c.api.Store.GetAgentByServer(ctx, id)
	if err != nil || ag.AppliedRevision >= clean.Revision {
		t.Fatal("old agent acknowledged cleanup", err)
	}
	call("POST", agentproto.PathApplyReport, "1", clean, 204, nil)
	rec, err := c.api.Store.LatestDesiredState(ctx, id)
	if err != nil || rec.Status != domain.DesiredApplied {
		t.Fatal("negotiated cleanup did not apply", err)
	}
}
