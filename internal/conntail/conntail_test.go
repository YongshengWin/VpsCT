package conntail

import (
	"ctlvps/internal/agentproto"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		line string
		ok   bool
		host string
		port int
		net  string
		node int64
	}{
		{"+0800 2026-03-01 12:00:00 INFO [2894612563 0ms] inbound/vless[node-12]: inbound connection to www.google.com:443", true, "www.google.com", 443, "tcp", 12},
		{"+0000 2026-03-01 12:00:00 INFO [1 0ms] inbound/hysteria2[node-7]: inbound packet connection to 8.8.8.8:53", true, "8.8.8.8", 53, "udp", 7},
		{"+0000 2026-03-01 12:00:00 INFO [1 0ms] inbound/trojan[node-3]: inbound connection to [2001:db8::1]:443", true, "2001:db8::1", 443, "tcp", 3},
		{"+0000 2026-03-01 12:00:00 INFO [1 0ms] inbound/vless[node-12]: inbound connection from 1.2.3.4:5555", false, "", 0, "", 0}, // from is paired separately
		{"+0000 2026-03-01 12:00:00 INFO [1 0ms] outbound/direct[direct]: outbound connection to x:1", false, "", 0, "", 0},
		{"garbage", false, "", 0, "", 0},
	}
	for _, c := range cases {
		ev, ok := Parse(c.line, now)
		if ok != c.ok {
			t.Fatalf("%q: ok=%v", c.line, ok)
		}
		if !ok {
			continue
		}
		if ev.DestHost != c.host || ev.DestPort != c.port || ev.Network != c.net || ev.NodeID != c.node {
			t.Fatalf("%q: %+v", c.line, ev)
		}
		if ev.TS.Year() != 2026 || ev.TS.Month() != 3 {
			t.Fatalf("timestamp not parsed: %v", ev.TS)
		}
	}
}

func TestParseUserPrefixedTo(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to, ok := parseLine("+0000 2026-03-01 12:00:00 INFO [88 51ms] inbound/vless[node-12]: [0] inbound connection to www.google.com:443", now)
	if !ok || to.dir != "to" || to.ev.DestHost != "www.google.com" || to.ev.DestPort != 443 {
		t.Fatalf("user-prefixed to: %+v ok=%v", to, ok)
	}
	from, ok := parseLine("+0000 2026-03-01 12:00:00 INFO [77] inbound/vless[node-12]: inbound connection from 203.0.113.9:5555", now)
	if !ok || from.ev.SrcHost != "203.0.113.9" {
		t.Fatalf("id-only from: %+v ok=%v", from, ok)
	}
}

func TestParsePairsClient(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	from, ok := parseLine("+0000 2026-03-01 12:00:00 INFO [99 0ms] inbound/vless[node-12]: inbound connection from 203.0.113.9:5555", now)
	if !ok || from.dir != "from" || from.ev.SrcHost != "203.0.113.9" || from.connID != "99" {
		t.Fatalf("from: %+v ok=%v", from, ok)
	}
	to, ok := parseLine("+0000 2026-03-01 12:00:00 INFO [99 0ms] inbound/vless[node-12]: inbound connection to www.google.com:443", now)
	if !ok || to.dir != "to" || to.ev.DestHost != "www.google.com" {
		t.Fatalf("to: %+v ok=%v", to, ok)
	}
}

func TestTailerFollowsAndTruncates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sb.log")
	if err := os.WriteFile(path, []byte("old line that must be skipped\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tl := New(path)
	tl.MaxLogBytes = 200
	allowed := map[int64]bool{12: true}
	tl.Allowed = func(id int64) bool { return allowed[id] }
	st, _ := os.Stat(path)
	tl.offset = st.Size()
	tl.inode = inodeOf(st)

	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString("+0000 2026-03-01 12:00:00 INFO [1 0ms] inbound/vless[node-12]: inbound connection from 198.51.100.8:1000\n")
	_, _ = f.WriteString("+0000 2026-03-01 12:00:00 INFO [1 0ms] inbound/vless[node-12]: inbound connection to a.example:443\n")
	_, _ = f.WriteString("+0000 2026-03-01 12:00:00 INFO [1 0ms] inbound/vless[node-99]: inbound connection to b.example:443\n")
	_, _ = f.WriteString("+0000 2026-03-01 12:00:00 INFO [1 0ms] inbound/vless[node-12]: inbound connection to c.exam") // partial
	f.Close()
	tl.poll()
	if n, _ := tl.Pending(); n != 1 {
		t.Fatalf("expected 1 event (node-99 filtered, partial line kept), got %d", n)
	}
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString("ple:443\n")
	f.Close()
	tl.poll()
	evs := tl.Take(10)
	if len(evs) != 2 || evs[1].DestHost != "c.example" {
		t.Fatalf("events: %+v", evs)
	}
	if evs[0].SrcHost != "198.51.100.8" || evs[0].DestHost != "a.example" {
		t.Fatalf("client not paired: %+v", evs[0])
	}
	// file exceeded MaxLogBytes and we are caught up -> truncated
	st, _ = os.Stat(path)
	if st.Size() != 0 || tl.offset != 0 {
		t.Fatalf("expected truncation, size=%d offset=%d", st.Size(), tl.offset)
	}
	tl.Requeue(evs)
	if n, _ := tl.Pending(); n != 2 {
		t.Fatal("requeue")
	}
}

func TestVLESSDifferentIDsUseLastSrc(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sb.log")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	tl := New(path)
	st, _ := os.Stat(path)
	tl.offset = st.Size()
	tl.inode = inodeOf(st)
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString("+0000 2026-03-01 12:00:00 INFO [111 0ms] inbound/vless[node-12]: inbound connection from 198.51.100.8:1000\n")
	_, _ = f.WriteString("+0000 2026-03-01 12:00:00 INFO [222 12ms] inbound/vless[node-12]: [0] inbound connection to a.example:443\n")
	f.Close()
	tl.poll()
	evs := tl.Take(10)
	if len(evs) != 1 || evs[0].SrcHost != "198.51.100.8" || evs[0].DestHost != "a.example" {
		t.Fatalf("vless pair: %+v", evs)
	}
}

func TestToBeforeFromSameID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sb.log")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	tl := New(path)
	st, _ := os.Stat(path)
	tl.offset = st.Size()
	tl.inode = inodeOf(st)
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString("+0000 2026-03-01 12:00:00 INFO [9 0ms] inbound/hysteria2[node-3]: inbound connection to b.example:443\n")
	f.Close()
	tl.poll()
	if n, _ := tl.Pending(); n != 0 {
		t.Fatalf("to should wait for from, got %d", n)
	}
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString("+0000 2026-03-01 12:00:00 INFO [9 0ms] inbound/hysteria2[node-3]: inbound connection from 203.0.113.1:9\n")
	f.Close()
	tl.poll()
	evs := tl.Take(10)
	if len(evs) != 1 || evs[0].SrcHost != "203.0.113.1" || evs[0].DestHost != "b.example" {
		t.Fatalf("late from: %+v", evs)
	}
}

func TestBufferByteBudgetIncludesRequeue(t *testing.T) {
	tail := New("")
	tail.MaxBufferBytes = 1024
	batch := []agentproto.ConnEvent{}
	for j := 0; j < 100; j++ {
		batch = append(batch, agentproto.ConnEvent{DestHost: strings.Repeat("x", 256)})
	}
	tail.Requeue(batch)
	if tail.bufferedBytes > 1024 || tail.count > 2 {
		t.Fatal("buffer exceeded byte budget")
	}
	taken := tail.Take(1)
	tail.Requeue(taken)
	if tail.bufferedBytes > 1024 {
		t.Fatal("retry exceeded byte budget")
	}
	tail.Take(100)
	if tail.bufferedBytes != 0 {
		t.Fatal("draining left phantom bytes")
	}
}

func TestRingWrapAndRetryKeepsNewest(t *testing.T) {
	tail := New("")
	tail.MaxEvents = 4
	for i := 1; i <= 6; i++ {
		tail.pushLocked(agentproto.ConnEvent{NodeID: int64(i)})
	}
	batch := tail.Take(2)
	if batch[0].NodeID != 3 || batch[1].NodeID != 4 {
		t.Fatal(batch)
	}
	tail.pushLocked(agentproto.ConnEvent{NodeID: 7})
	tail.Requeue(batch)
	got := tail.Take(10)
	for i, want := range []int64{4, 5, 6, 7} {
		if got[i].NodeID != want {
			t.Fatal(got)
		}
	}
	if tail.buf != nil || tail.bufferedBytes != 0 {
		t.Fatal("drained queue retained storage")
	}
}

func TestOversizedPartialLineAndPollBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	// An oversized backlog is skipped, and its unterminated tail discarded.
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 3<<20)), 0600); err != nil {
		t.Fatal(err)
	}
	tail := New(path)
	tail.poll()
	if tail.offset != 3<<20 {
		t.Fatalf("old backlog was not skipped: %d", tail.offset)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString("\n+0000 2026-03-01 12:00:00 INFO [1] inbound/vless[node-1]: inbound connection from 192.0.2.1:1\n+0000 2026-03-01 12:00:00 INFO [1] inbound/vless[node-1]: inbound connection to example.test:443\n")
	f.Close()
	tail.poll()
	got := tail.Take(10)
	if len(got) != 1 || got[0].DestHost != "example.test" {
		t.Fatal(got)
	}
}

func TestDisableReleasesBacklogAndIdleExpiresPairs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	os.WriteFile(path, nil, 0600)
	tail := New(path)
	tail.byID["stale"] = half{at: time.Now().Add(-3 * time.Minute)}
	tail.lastSrc[1] = fromHint{at: time.Now().Add(-3 * time.Minute)}
	tail.poll()
	if len(tail.byID) != 0 || len(tail.lastSrc) != 0 {
		t.Fatal("idle pair retained")
	}
	tail.pushLocked(agentproto.ConnEvent{DestHost: "example.test"})
	tail.Enabled = func() bool { return false }
	tail.poll()
	if tail.count != 0 || tail.buf != nil {
		t.Fatal("disabled queue retained")
	}
}

func BenchmarkBacklogDrainRetry(b *testing.B) {
	tail := New("")
	for i := 0; i < 20000; i++ {
		tail.pushLocked(agentproto.ConnEvent{NodeID: int64(i), DestHost: "example.test"})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		batch := tail.Take(500)
		tail.Requeue(batch)
	}
}

func TestSharedQueueSustainedOutageAndDrain(t *testing.T) {
	q := NewSharedQueue()
	a, b := New(""), New("")
	a.Queue = q
	b.Queue = q
	// Two readers share one budget even while uploading is unavailable.
	for round := 0; round < 100000; round++ {
		tail := a
		if round%2 != 0 {
			tail = b
		}
		tail.mu.Lock()
		tail.pushLocked(agentproto.ConnEvent{NodeID: int64(round), DestHost: strings.Repeat("x", 1024)})
		tail.mu.Unlock()
	}
	count, _ := q.Pending()
	if count > q.MaxEvents || q.bufferedBytes > q.MaxBufferBytes {
		t.Fatal("shared outage budget exceeded")
	}
	if n, _ := a.Pending(); n != count {
		t.Fatal("reader does not use shared queue")
	}
	for count > 0 {
		batch := b.Take(500)
		count -= len(batch)
	}
	if q.bufferedBytes != 0 {
		t.Fatal("queue not drained")
	}
	for _, ev := range q.buf {
		if ev.DestHost != "" {
			t.Fatal("drained ring retains payload")
		}
	}
}
