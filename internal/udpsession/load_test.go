package udpsession

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBatchExpiryPreservesLiveSessionsAndBoundsConcurrency(t *testing.T) {
	var mu sync.Mutex
	items := []Connection{}
	for i := 1; i <= 1024; i++ {
		items = append(items, connection(fmt.Sprintf("%08x-1111-1111-1111-111111111111", i), "direct/forward-1", "udp", "192.0.2.2", fmt.Sprint(10000+i)))
	}
	items = append(items, connection("node", "shadowsocks/node-1", "udp", "192.0.2.2", "99"))
	removed := map[string]bool{}
	inflight, peak := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			mu.Lock()
			inflight++
			peak = max(peak, inflight)
			mu.Unlock()
			time.Sleep(4 * time.Millisecond)
			mu.Lock()
			removed[strings.TrimPrefix(r.URL.Path, "/connections/")] = true
			inflight--
			mu.Unlock()
			w.WriteHeader(204)
			return
		}
		mu.Lock()
		var snapshot []Connection
		for _, c := range items {
			if !removed[c.ID] {
				snapshot = append(snapshot, c)
			}
		}
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"connections": snapshot})
	}))
	defer srv.Close()
	api, err := NewAPI(strings.TrimPrefix(srv.URL, "http://"), strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Policies: map[int64]Policy{1: {25001, 2 * time.Second}}}
	live := map[Flow]bool{}
	for i := 1; i <= 32; i++ {
		live[Flow{1, netip.MustParseAddrPort(fmt.Sprintf("192.0.2.2:%d", 10000+i)), 25001}] = true
	}
	var reaper Reaper
	now := time.Now()
	if err = reaper.Sweep(context.Background(), api, cfg, live, now); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err = reaper.Sweep(context.Background(), api, cfg, live, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(removed) != MaxExpiryBatch || peak > 8 || peak < 2 || reaper.Active[1] != 512 || reaper.Reaped != MaxExpiryBatch {
		t.Fatal("bad bounded expiry batch", len(removed), peak, reaper.Active, reaper.Reaped)
	}
	mu.Unlock()
	if err = reaper.Sweep(context.Background(), api, cfg, live, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(removed) != 992 || reaper.Active[1] != 32 || reaper.Reaped != 992 {
		t.Fatal("deferred expiry lost its observations", len(removed), reaper.Active, reaper.Reaped)
	}
	for i := 1; i <= 32; i++ {
		if removed[items[i-1].ID] {
			t.Fatal("live connection reaped")
		}
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("bounded batch failed lease time budget")
	}
}
