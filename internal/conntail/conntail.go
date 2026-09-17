// Package conntail follows the sing-box log, extracts destination
// connections per inbound (node) and batches them for upload.
package conntail

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"ctlvps/internal/agentbudget"
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
	if m == nil || len(m[1]) > 32 {
		return parsedLine{}, false
	}
	id, err := strconv.ParseInt(m[3], 10, 64)
	if err != nil {
		return parsedLine{}, false
	}
	host, port := splitAddr(m[6])
	if host == "" || len(host) > 1024 {
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
	Queue          *Tailer // optional common event queue; pairing remains local
	fixed          bool
	Path           string
	MaxLogBytes    int64 // truncate the log once it grows beyond this (sing-box opens with O_APPEND)
	MaxEvents      int   // ring buffer size
	MaxBufferBytes int64
	bufferedBytes  int64
	Enabled        func() bool
	// Only node ids present here are recorded (nil = all).
	Allowed func(nodeID int64) bool

	head, count int
	skipping    bool // discard an oversized line through its newline
	mu          sync.Mutex
	buf         []agentproto.ConnEvent
	dropped     int64
	offset      int64
	inode       uint64
	byID        map[string]half // node:connID — same-id from/to
	lastSrc     map[int64]fromHint
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
	return &Tailer{Path: path, MaxLogBytes: 64 << 20, MaxEvents: 20000, MaxBufferBytes: 8 << 20, Enabled: func() bool { return true }, byID: map[string]half{}, lastSrc: map[int64]fromHint{}}
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
	const maxLine = 16 << 10
	const maxPoll = 1 << 20
	enabled := t.Enabled == nil || t.Enabled()
	if !enabled {
		t.clearQueue()
		t.mu.Lock()
		clear(t.byID)
		clear(t.lastSrc)
		t.mu.Unlock()
	}
	st, err := os.Stat(t.Path)
	if err != nil {
		t.expire(time.Now())
		return
	}
	if ino := inodeOf(st); ino != t.inode || st.Size() < t.offset {
		t.inode = ino
		t.offset = 0
		t.skipping = false
		t.mu.Lock()
		clear(t.byID)
		clear(t.lastSrc)
		t.mu.Unlock()
	}
	if !enabled {
		t.offset = st.Size()
		t.skipping = false
		t.maybeTruncate(st.Size())
		return
	}
	if st.Size() == t.offset {
		t.expire(time.Now())
		t.maybeTruncate(st.Size())
		return
	}
	// Connection history is best-effort; skip an old backlog rather than
	// letting sustained writes keep this file permanently beyond its disk cap.
	if st.Size()-t.offset > maxPoll {
		t.offset = st.Size() - maxPoll
		t.skipping = true
		t.mu.Lock()
		clear(t.byID)
		clear(t.lastSrc)
		t.mu.Unlock()
	}
	f, err := os.Open(t.Path)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return
	}
	// Snapshot a finite amount of work: a continuously growing file cannot
	// monopolize the reader. ReadSlice never allocates an unbounded line.
	r := bufio.NewReaderSize(io.LimitReader(f, min(st.Size()-t.offset, maxPoll)), maxLine)
	now := time.Now()
	for {
		line, err := r.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			t.offset += int64(len(line))
			t.skipping = true
			continue
		}
		if t.skipping {
			t.offset += int64(len(line))
			if err == nil {
				t.skipping = false
			}
			if err != nil {
				break
			}
			continue
		}
		if err != nil {
			break
		} // retry a bounded partial line next poll
		t.offset += int64(len(line))
		p, ok := parseLine(strings.TrimRight(string(line), "\r\n"), now)
		if !ok || (t.Allowed != nil && !t.Allowed(p.ev.NodeID)) {
			continue
		}
		// Parsed substrings must not retain the entire original log line.
		p.connID = strings.Clone(p.connID)
		p.ev.SrcHost = strings.Clone(p.ev.SrcHost)
		p.ev.DestHost = strings.Clone(p.ev.DestHost)
		t.ingest(p, now)
	}
	t.expire(now)
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

func (t *Tailer) expire(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, h := range t.byID {
		if now.Sub(h.at) > 2*time.Minute {
			if h.hasDest {
				t.pushLocked(h.ev)
			}
			delete(t.byID, k)
		}
	}
	for k, h := range t.lastSrc {
		if now.Sub(h.at) > 2*time.Minute {
			delete(t.lastSrc, k)
		}
	}
}

func (t *Tailer) gcLocked(now time.Time) {
	if len(t.byID) < agentbudget.PairEntriesPerTail && len(t.lastSrc) < agentbudget.PairEntriesPerTail {
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
	if len(t.byID) >= agentbudget.PairEntriesPerTail {
		t.byID = map[string]half{}
	}
	if len(t.lastSrc) >= agentbudget.PairEntriesPerTail {
		t.lastSrc = map[int64]fromHint{}
	}
}

func eventBytes(ev agentproto.ConnEvent) int64 {
	return int64(128 + len(ev.SrcHost) + len(ev.DestHost))
}

// The ring grows lazily, never beyond the event ceiling. Taking or retrying
// a batch does not copy the backlog, and vacated slots release their strings.
func (t *Tailer) roomLocked() {
	if t.count < len(t.buf) {
		return
	}
	size := min(max(16, len(t.buf)*2), max(1, t.MaxEvents))
	b := make([]agentproto.ConnEvent, size)
	for i := 0; i < t.count; i++ {
		b[i] = t.buf[(t.head+i)%len(t.buf)]
	}
	t.buf = b
	t.head = 0
}
func (t *Tailer) popLocked() agentproto.ConnEvent {
	ev := t.buf[t.head]
	t.buf[t.head] = agentproto.ConnEvent{}
	t.head = (t.head + 1) % len(t.buf)
	t.count--
	t.bufferedBytes -= eventBytes(ev)
	return ev
}
func (t *Tailer) pushLocked(ev agentproto.ConnEvent) {
	if t.Queue != nil {
		t.Queue.mu.Lock()
		defer t.Queue.mu.Unlock()
		t.Queue.pushLocked(ev)
		return
	}
	size := eventBytes(ev)
	if t.MaxBufferBytes > 0 && size > t.MaxBufferBytes {
		t.dropped++
		return
	}
	for t.count > 0 && (t.count >= max(1, t.MaxEvents) || (t.MaxBufferBytes > 0 && t.bufferedBytes+size > t.MaxBufferBytes)) {
		t.popLocked()
		t.dropped++
	}
	t.roomLocked()
	t.buf[(t.head+t.count)%len(t.buf)] = ev
	t.count++
	t.bufferedBytes += size
}

// Take removes up to n events, releasing only the consumed ring slots.
func (t *Tailer) Take(n int) []agentproto.ConnEvent {
	if t.Queue != nil {
		return t.Queue.Take(n)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	n = max(0, min(n, t.count))
	out := make([]agentproto.ConnEvent, n)
	for i := range out {
		out[i] = t.popLocked()
	}
	if t.count == 0 && !t.fixed {
		t.buf = nil
		t.head = 0
	}
	return out
}

// Requeue preserves order and keeps the newest events when retrying a full
// queue. It never allocates a second copy of the backlog.
func (t *Tailer) Requeue(evs []agentproto.ConnEvent) {
	if t.Queue != nil {
		t.Queue.Requeue(evs)
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := len(evs) - 1; i >= 0; i-- {
		ev := evs[i]
		size := eventBytes(ev)
		if t.count >= max(1, t.MaxEvents) || (t.MaxBufferBytes > 0 && t.bufferedBytes+size > t.MaxBufferBytes) {
			t.dropped += int64(i + 1)
			break
		}
		t.roomLocked()
		t.head = (t.head + len(t.buf) - 1) % len(t.buf)
		t.buf[t.head] = ev
		t.count++
		t.bufferedBytes += size
	}
}

// Pending returns the buffered count and the number of dropped events.
func (t *Tailer) Pending() (int, int64) {
	if t.Queue != nil {
		return t.Queue.Pending()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.count, t.dropped
}
