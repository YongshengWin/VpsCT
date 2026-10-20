package udpsession

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func connection(id, kind, network, source string, port string) Connection {
	c := Connection{ID: id}
	c.Metadata.Type = kind
	c.Metadata.Network = network
	c.Metadata.SourceIP = source
	c.Metadata.SourcePort = port
	return c
}

func TestReaperClosesOnlyExpiredUDPForwardAndPreservesKernelActivity(t *testing.T) {
	ctx := context.Background()
	secret := strings.Repeat("a", 64)
	id1, id2 := "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
	items := []Connection{connection(id1, "direct/forward-1", "udp", "192.0.2.2", "44000"), connection(id2, "direct/forward-2", "udp", "2001:db8::2", "44001"), connection("node", "shadowsocks/node-1", "udp", "192.0.2.2", "44002"), connection("tcp", "direct/forward-1", "tcp", "192.0.2.2", "44003")}
	var closed []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			t.Error("missing local authentication")
			w.WriteHeader(401)
			return
		}
		if r.Method == "DELETE" {
			closed = append(closed, strings.TrimPrefix(r.URL.Path, "/connections/"))
			w.WriteHeader(204)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"connections": items})
	}))
	defer server.Close()
	api, err := NewAPI(strings.TrimPrefix(server.URL, "http://"), secret)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Policies: map[int64]Policy{1: {25001, 2 * time.Second}, 2: {25002, 2 * time.Second}}}
	live := map[Flow]bool{{2, netip.MustParseAddrPort("[2001:db8::2]:44001"), 25002}: true}
	r := Reaper{}
	now := time.Now()
	if err = r.Sweep(ctx, api, cfg, live, now); err != nil {
		t.Fatal(err)
	}
	if len(closed) != 0 {
		t.Fatal("closed on first observation")
	}
	// Byte counters unchanged, but the kernel still admits empty/one-way UDP.
	if err = r.Sweep(ctx, api, cfg, live, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(closed) != 1 || closed[0] != id1 {
		t.Fatal("wrong sessions closed", closed)
	}
	closed = nil
	items = items[:1]
	r = Reaper{}
	if err = r.Sweep(ctx, api, cfg, nil, now); err != nil {
		t.Fatal(err)
	}
	items[0].Download = 1
	if err = r.Sweep(ctx, api, cfg, nil, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(closed) != 0 {
		t.Fatal("closed after intervening downstream traffic")
	}
	if err = r.Sweep(ctx, api, cfg, map[Flow]bool{{1, netip.MustParseAddrPort("192.0.2.2:44000"), 25001}: true}, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(closed) != 0 {
		t.Fatal("closed re-admitted session")
	}
}

func TestLocalManagerRejectsMissingAuthenticationAndIncompleteSnapshots(t *testing.T) {
	for _, address := range []string{"0.0.0.0:9090", "localhost:9090", "[::1]:9090", "127.0.0.1:0", "127.0.0.1:bad"} {
		if _, err := NewAPI(address, strings.Repeat("a", 64)); err == nil {
			t.Fatal("unsafe endpoint", address)
		}
	}
	for _, body := range []string{`{}`, `{"connections":[]`, `{"connections":[],"connections":[]}`, `{"connections":{}}`, `{"connections":[]} {}`} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		api, _ := NewAPI(strings.TrimPrefix(s.URL, "http://"), strings.Repeat("b", 64))
		if _, err := api.Connections(context.Background(), nil); err == nil {
			t.Error("invalid snapshot accepted", body)
		}
		s.Close()
	}
	api, _ := NewAPI("127.0.0.1:9090", strings.Repeat("b", 64))
	if api.Close(context.Background(), "../configs") == nil {
		t.Fatal("arbitrary API path admitted")
	}
}

func TestConfigRequiresExactLocalAPIAndForwardTimeout(t *testing.T) {
	body := `{"experimental":{"clash_api":{"external_controller":"127.0.0.1:0","secret":"` + strings.Repeat("a", 64) + `"}},"inbounds":[{"tag":"forward-1","network":["tcp","udp"],"listen_port":25001,"udp_timeout":"2s"},{"tag":"node-1","listen_port":25002}]}`
	cfg, err := ParseConfig([]byte(body))
	if err != nil || len(cfg.Policies) != 1 || !cfg.ListenPorts[25002] {
		t.Fatal(cfg, err)
	}
	udpOnly := strings.Replace(body, `["tcp","udp"]`, `"udp"`, 1)
	cfg, err = ParseConfig([]byte(udpOnly))
	if err != nil || cfg.ListenPorts[25001] || len(cfg.Policies) != 1 {
		t.Fatal("UDP-only port wrongly excluded from TCP API discovery", cfg, err)
	}
	for _, bad := range []string{strings.Replace(body, "127.0.0.1:0", "0.0.0.0:9090", 1), strings.Replace(body, "2s", "1s", 1), strings.Replace(body, strings.Repeat("a", 64), "short", 1)} {
		if _, err = ParseConfig([]byte(bad)); err == nil {
			t.Fatal("invalid local policy accepted")
		}
	}
}
