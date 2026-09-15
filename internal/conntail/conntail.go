// Package conntail follows the sing-box log, extracts destination
// connections per inbound (node) and batches them for upload.
package conntail

import (
	"bufio"
	"context"
	"io"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"ctlvps/internal/agentproto"
)

// lineRe matches sing-box inbound from/to lines. VLESS Reality often logs
// "[user] inbound connection to" and a different conn id than the from line.
var lineRe = regexp.MustCompile(`\[(\d+)(?:\s+[0-9.]+(?:ms|s))?\] inbound/([a-z0-9-]+)\[node-(\d+)\]: (?:\[[^\]]+\] )?inbound (packet )?connection (from|to) (.+)\s*$`)

type parsedLine struct {
	ev     agentproto.ConnEvent
	connID string
	dir    string // from | to
}

func parseLine(line string, now time.Time) (parsedLine, bool) {
	m := lineRe.FindStringSubmatch(line)
	if m == nil {
		return parsedLine{}, false
	}
	id, err := strconv.ParseInt(m[3], 10, 64)
	if err != nil {
		return parsedLine{}, false
	}
	host, port := splitAddr(m[6])
	if host == "" {
		return parsedLine{}, false
	}
	network := "tcp"
	if m[4] != "" {
		network = "udp"
	}
	ts := now
	if len(line) > 25 {
		if t, err := time.Parse("-0700 2006-01-02 15:04:05", line[:25]); err == nil {
			ts = t
		}
	}
	ev := agentproto.ConnEvent{TS: ts.UTC(), NodeID: id, Network: network}
	if m[5] == "from" {
		ev.SrcHost = host
	} else {
		ev.DestHost = host
		ev.DestPort = port
	}
	return parsedLine{ev: ev, connID: m[1], dir: m[5]}, true
}

func splitAddr(s string) (host string, port int) {
	s = strings.TrimSpace(s)
	h, p, err := net.SplitHostPort(s)
	if err != nil {
		return strings.TrimSuffix(strings.TrimPrefix(s, "["), "]"), 0
	}
	port, _ = strconv.Atoi(p)
	return h, port
}

// Parse extracts a destination event from one log line.
func Parse(line string, now time.Time) (agentproto.ConnEvent, bool) {
	p, ok := parseLine(line, now)
	if !ok || p.dir != "to" {
		return agentproto.ConnEvent{}, false
	}
	return p.ev, true
}

// Tailer follows a file and buffers parsed events.
type Tailer struct {
	Path        string
	MaxLogBytes int64 // truncate the log once it grows beyond this (sing-box opens with O_APPEND)
	MaxEvents   int   // ring buffer size
	Enabled     func() bool
	// Only node ids present here are recorded (nil = all).
	Allowed func(nodeID int64) bool

	mu      sync.Mutex
	buf     []agentproto.ConnEvent
	dropped int64
	offset  int64
	inode   uint64
	byID    map[string]half // node:connID — same-id from/to
	lastSrc map[int64]fromHint
}

type fromHint struct {
	src string
	at  time.Time
}

type half struct {
	ev      agentproto.ConnEvent
	hasSrc  bool
	hasDest bool
	at      time.Time
}

// New builds a tailer.
func New(path string) *Tailer {
	return &Tailer{Path: path, MaxLogBytes: 64 << 20, MaxEvents: 200000, Enabled: func() bool { return true }, byID: map[string]half{}, lastSrc: map[int64]fromHint{}}
}

// Run follows the file until ctx is done.
func (t *Tailer) Run(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	// start at end of the current file so we do not replay history
	if st, err := os.Stat(t.Path); err == nil {
		t.offset = st.Size()
		t.inode = inodeOf(st)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.poll()
		}
	}
}

func (t *Tailer) poll() {
	st, err := os.Stat(t.Path)
	if err != nil {
		return
	}
	if ino := inodeOf(st); ino != t.inode || st.Size() < t.offset {
		t.inode = ino
		t.offset = 0
	}
	if st.Size() == t.offset {
		t.maybeTruncate(st.Size())
		return
	}
	f, err := os.Open(t.Path)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return
	}
	r := bufio.NewReaderSize(f, 256<<10)
	now := time.Now()
	enabled := t.Enabled == nil || t.Enabled()
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			// partial line: leave it for next poll
			break
		}
		t.offset += int64(len(line))
		if !enabled {
			continue
		}
		p, ok := parseLine(strings.TrimRight(line, "\r\n"), now)
		if !ok || (t.Allowed != nil && !t.Allowed(p.ev.NodeID)) {
			continue
		}
		t.ingest(p, now)
	}
	t.flushStale(now)
	t.maybeTruncate(st.Size())
}

func (t *Tailer) maybeTruncate(size int64) {
	if t.MaxLogBytes > 0 && size >= t.MaxLogBytes && t.offset >= size {
		if err := os.Truncate(t.Path, 0); err == nil {
			t.offset = 0
		}
	}
}

func (t *Tailer) ingest(p parsedLine, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.byID == nil {
		t.byID = map[string]half{}
	}
	if t.lastSrc == nil {
		t.lastSrc = map[int64]fromHint{}
	}
	key := strconv.FormatInt(p.ev.NodeID, 10) + ":" + p.connID
	if p.dir == "from" {
		t.lastSrc[p.ev.NodeID] = fromHint{src: p.ev.SrcHost, at: now}
		h := t.byID[key]
		h.ev.NodeID = p.ev.NodeID
		h.ev.Network = p.ev.Network
		h.ev.TS = p.ev.TS
		h.ev.SrcHost = p.ev.SrcHost
		h.hasSrc = true
		h.at = now
		if h.hasDest {
			t.pushLocked(h.ev)
			delete(t.byID, key)
			return
		}
		t.byID[key] = h
		t.gcLocked(now)
		return
	}
	src := ""
	if h, ok := t.byID[key]; ok && h.hasSrc {
		src = h.ev.SrcHost
		delete(t.byID, key)
	} else if hint, ok := t.lastSrc[p.ev.NodeID]; ok && now.Sub(hint.at) < 2*time.Minute {
		// VLESS Reality: from uses the accept id, to uses a new id after auth.
		src = hint.src
	}
	p.ev.SrcHost = src
	if src != "" {
		t.pushLocked(p.ev)
		return
	}
	h := t.byID[key]
	h.ev = p.ev
	h.hasDest = true
	h.at = now
	t.byID[key] = h
	t.gcLocked(now)
}

func (t *Tailer) flushStale(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	cutoff := now.Add(-2 * time.Second)
	for k, h := range t.byID {
		if !h.hasDest || !h.at.Before(cutoff) {
			continue
		}
		t.pushLocked(h.ev)
		delete(t.byID, k)
	}
}

func (t *Tailer) gcLocked(now time.Time) {
	if len(t.byID) < 4096 && len(t.lastSrc) < 4096 {
		return
	}
	cutoff := now.Add(-2 * time.Minute)
	for k, h := range t.byID {
		if h.at.Before(cutoff) {
			if h.hasDest {
				t.pushLocked(h.ev)
			}
			delete(t.byID, k)
		}
	}
	for id, h := range t.lastSrc {
		if h.at.Before(cutoff) {
			delete(t.lastSrc, id)
		}
	}
	if len(t.byID) >= 4096 {
		t.byID = map[string]half{}
	}
	if len(t.lastSrc) >= 4096 {
		t.lastSrc = map[int64]fromHint{}
	}
}

func (t *Tailer) pushLocked(ev agentproto.ConnEvent) {
	if len(t.buf) >= t.MaxEvents {
		drop := len(t.buf) / 10
		t.buf = t.buf[drop:]
		t.dropped += int64(drop)
	}
	t.buf = append(t.buf, ev)
}

// Take removes up to n events from the buffer.
func (t *Tailer) Take(n int) []agentproto.ConnEvent {
	t.mu.Lock()
	defer t.mu.Unlock()
	if n > len(t.buf) {
		n = len(t.buf)
	}
	out := make([]agentproto.ConnEvent, n)
	copy(out, t.buf[:n])
	t.buf = append([]agentproto.ConnEvent(nil), t.buf[n:]...)
	return out
}

// Requeue puts events back at the front (after a failed upload).
func (t *Tailer) Requeue(evs []agentproto.ConnEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(append([]agentproto.ConnEvent(nil), evs...), t.buf...)
	if len(t.buf) > t.MaxEvents {
		t.dropped += int64(len(t.buf) - t.MaxEvents)
		t.buf = t.buf[:t.MaxEvents]
	}
}

// Pending returns the buffered count and the number of dropped events.
func (t *Tailer) Pending() (int, int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.buf), t.dropped
}
