package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"ctlvps/internal/auth"
	"ctlvps/internal/domain"
)

func TestSubscriptionClientProfileAcrossEntryPoints(t *testing.T) {
	c := setupAPIKeyTest(t)
	key, _ := issueTestAPIKey(t, c, 1, "read", "write")
	nodes := []domain.Node{
		{Name: "simple", Protocol: "http", Server: "simple.invalid", Port: 8080},
		{Name: "modern", Protocol: "vless", Server: "modern.invalid", Port: 443, Params: json.RawMessage(`{"uuid":"11223344-5566-7788-9900-aabbccddeeff","tls":true,"network":"xhttp","servername":"modern.invalid","xhttp-opts":{"path":"/check","mode":"packet-up"}}`)},
	}
	for i := range nodes {
		nodes[i].Source, nodes[i].Enabled = domain.NodeManual, true
		if err := c.api.Store.CreateNode(t.Context(), &nodes[i]); err != nil {
			t.Fatal(err)
		}
	}
	token := auth.NewSubscriptionToken()
	sub := domain.Subscription{Name: "preview", Kind: domain.SubGenerated, Enabled: true, OwnerUserID: 1, Token: token, TokenHash: auth.HashToken(token), ShortCode: "profile-check", DefaultFormat: "surge", NodeSelection: domain.NodeSelection{IncludeAll: true}}
	if err := c.api.Store.CreateSubscription(t.Context(), &sub); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/subscriptions/%d/render?format=surge", sub.ID)
	legacy, _ := keyRequest(t, c, "GET", path, key, nil, nil, 200)
	modern, _ := keyRequest(t, c, "GET", path+"&profile=surge-vless", key, nil, nil, 200)
	if legacy["node_count"] != float64(1) || legacy["omitted_count"] != float64(1) || modern["node_count"] != float64(2) || modern["omitted_count"] != float64(0) {
		t.Fatal("saved counts differ from the selected client profile")
	}
	draft, _ := keyRequest(t, c, "POST", "/api/v1/subscriptions/preview", key, map[string]any{"name": "draft", "format": "surge", "profile": "surge-vless", "node_selection": sub.NodeSelection}, nil, 200)
	if len(draft["node_names"].([]any)) != 2 || !strings.Contains(draft["body"].(string), "xhttp-path=/check") {
		t.Fatal("draft lost the XHTTP node or its path")
	}
	for _, tc := range []struct {
		query  string
		status int
		modern bool
	}{{"?format=surge", 200, false}, {"?format=surge&profile=surge-vless", 200, true}, {"?format=mihomo&profile=surge-vless", 400, false}, {"?format=surge&profile=unknown", 400, false}} {
		resp, err := http.Get(c.srv.URL + "/r/" + sub.ShortCode + tc.query)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != tc.status || strings.Contains(string(body), "xhttp-path=/check") != tc.modern || resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("public profile mismatch", tc.query, resp.StatusCode)
		}
		if tc.modern && !strings.Contains(strings.SplitN(string(body), "\n", 2)[0], "profile=surge-vless") {
			t.Fatal("managed URL lost selected profile")
		}
	}
}
