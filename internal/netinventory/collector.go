// Package netinventory observes interfaces independently of quota accounting.
package netinventory

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"ctlvps/internal/agentproto"
)

type link struct {
	agentproto.NetworkInterface
	Device string `json:"device,omitempty"`
}

type reading struct {
	Links    []link
	Complete bool
	Reset    bool
	Deleted  map[int]bool
}

type registry struct {
	Version  int       `json:"version"`
	ID       string    `json:"id"`
	BootID   string    `json:"boot_id"`
	Sequence int64     `json:"sequence"`
	At       time.Time `json:"at"`
	Links    []link    `json:"links"`
}

// Collector serializes heartbeat, billing and local guard observations. The
// registry is committed before returning, so a restart cannot reuse a sequence.
type Collector struct {
	mu           sync.Mutex
	preferredIDs map[string]bool
	bindingIDs   map[string]bool
	path, boot   string
	state        registry
	loaded       bool
	read         func() (reading, error)
	now          func() time.Time
	close        func() error
	invalid      map[int]bool
	resetPending bool
}

func New(stateDir, bootID string) *Collector {
	read, close := newSource()
	return &Collector{path: filepath.Join(stateDir, "network-interfaces.json"), boot: bootID, read: read, close: close, now: time.Now}
}

func (c *Collector) Close() error { c.mu.Lock(); defer c.mu.Unlock(); return c.close() }

func (c *Collector) SetPreferredIDs(ids map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.preferredIDs = copyIDs(ids)
}
func (c *Collector) SetBindingIDs(ids map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bindingIDs = copyIDs(ids)
}
func copyIDs(ids map[string]bool) map[string]bool {
	out := make(map[string]bool, len(ids))
	for id, on := range ids {
		if on {
			out[id] = true
		}
	}
	return out
}

func identity() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (c *Collector) load() error {
	if c.loaded {
		return nil
	}
	f, err := os.Open(c.path)
	if errors.Is(err, os.ErrNotExist) {
		c.state = registry{Version: 1, ID: identity()}
		c.loaded = true
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil || len(b) > 1<<20 {
		return errors.New("network registry unreadable")
	}
	var st registry
	if json.Unmarshal(b, &st) != nil || st.Version != 1 || len(st.ID) != 32 || st.Sequence < 1 || len(st.Links) > agentproto.MaxNetworkInterfaces {
		return errors.New("network registry invalid")
	}
	snapshot := agentproto.NetworkSnapshot{Version: 1, CollectorID: st.ID, BootID: st.BootID, Sequence: st.Sequence, SampledAt: st.At, Status: "ok"}
	for _, l := range st.Links {
		snapshot.Interfaces = append(snapshot.Interfaces, l.NetworkInterface)
	}
	if err := agentproto.ValidateNetwork(&snapshot); err != nil {
		return err
	}
	c.state, c.loaded = st, true
	return nil
}

func persist(path string, st registry) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".network-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func sameDevice(a, b link) bool {
	return a.Kind == b.Kind && a.MAC == b.MAC && a.Device == b.Device && a.ParentIndex == b.ParentIndex
}

func (c *Collector) Collect() *agentproto.NetworkSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	preferred := copyIDs(c.preferredIDs)
	for id := range c.bindingIDs {
		preferred[id] = true
	}
	at := c.now().UTC()
	s := &agentproto.NetworkSnapshot{Version: 1, BootID: c.boot, SampledAt: at, Status: "error", Interfaces: []agentproto.NetworkInterface{}}
	r, err := c.read()
	if c.invalid == nil {
		c.invalid = map[int]bool{}
	}
	for index := range r.Deleted {
		c.invalid[index] = true
	}
	if len(c.invalid) > 1024 {
		c.invalid = nil
		c.resetPending = true
	}
	c.resetPending = c.resetPending || r.Reset
	if errors.Is(err, errUnsupported) {
		s.Status = "unsupported"
		return s
	}
	if err != nil {
		s.Error = "无法读取网卡状态"
		return s
	}
	if err = c.load(); err != nil {
		s.Error = "无法读取网卡身份文件"
		return s
	}
	// An incomplete dump cannot prove a selected device disappeared. Reserve
	// identity slots before trimming the report, even if many new devices fill
	// the dump. Known deletions and complete dumps still retire missing IDs.
	var retained []link
	if !r.Complete && c.state.BootID == c.boot {
		present := map[int]bool{}
		for _, raw := range r.Links {
			present[raw.Index] = true
		}
		for _, old := range c.state.Links {
			if preferred[old.ID] && !present[old.Index] && !c.invalid[old.Index] && !(c.resetPending && (old.Device == "" || old.MAC == "")) {
				old.CountersValid, old.RateValid = false, false
				old.Rx, old.Tx, old.RxRate, old.TxRate = 0, 0, 0, 0
				retained = append(retained, old)
			}
		}
	}
	limit := agentproto.MaxNetworkInterfaces - len(retained)
	if len(r.Links) > limit {
		preferredIndices := map[int]bool{}
		for _, old := range c.state.Links {
			if preferred[old.ID] {
				for _, raw := range r.Links {
					if sameDevice(old, raw) && ((c.state.BootID == c.boot && old.Index == raw.Index) || (c.state.BootID != c.boot && raw.Device != "" && raw.MAC != "")) {
						preferredIndices[raw.Index] = true
					}
				}
			}
		}
		sort.SliceStable(r.Links, func(i, j int) bool { return preferredIndices[r.Links[i].Index] && !preferredIndices[r.Links[j].Index] })
		r.Links = r.Links[:limit]
		r.Complete = false
	}
	next := registry{Version: 1, ID: c.state.ID, BootID: c.boot, Sequence: c.state.Sequence + 1, At: at}
	used := map[string]bool{}
	for _, raw := range r.Links {
		l := raw
		l.ID, l.Generation = identity(), identity()
		l.RateValid, l.RxRate, l.TxRate = false, 0, 0
		var candidates []link
		for _, old := range c.state.Links {
			if !sameDevice(old, l) || used[old.ID] || c.invalid[old.Index] || (c.resetPending && (l.Device == "" || l.MAC == "")) {
				continue
			}
			if c.state.BootID == c.boot {
				if old.Index == l.Index {
					candidates = append(candidates, old)
				}
			} else if l.Device != "" && l.MAC != "" {
				// Only a stable device path plus MAC can preserve identity across boots.
				candidates = append(candidates, old)
			}
		}
		if len(candidates) == 1 {
			old := candidates[0]
			l.ID = old.ID
			used[old.ID] = true
			if !c.resetPending && c.state.BootID == c.boot && old.CountersValid && l.CountersValid && l.Rx >= old.Rx && l.Tx >= old.Tx {
				l.Generation = old.Generation
				if dt := at.Sub(c.state.At).Seconds(); dt >= 1 && dt <= 600 {
					l.RateValid = true
					l.RxRate, l.TxRate = int64(float64(l.Rx-old.Rx)/dt), int64(float64(l.Tx-old.Tx)/dt)
				}
			}
		}
		next.Links = append(next.Links, l)
		s.Interfaces = append(s.Interfaces, l.NetworkInterface)
	}
	s.Status = "ok"
	if !r.Complete {
		s.Status, s.Error = "incomplete", "部分网卡信息不可用或超过采集上限"
		next.Links = append(next.Links, retained...)
		// Do not forget identities omitted by an incomplete dump.
		for _, old := range c.state.Links {
			if c.invalid[old.Index] || (c.resetPending && (old.Device == "" || old.MAC == "")) {
				continue
			}
			if len(next.Links) >= agentproto.MaxNetworkInterfaces {
				break
			}
			found := false
			for _, l := range next.Links {
				if l.Index == old.Index {
					found = true
					break
				}
			}
			if !found && c.state.BootID == c.boot {
				old.CountersValid = false
				old.RateValid = false
				old.Rx, old.Tx, old.RxRate, old.TxRate = 0, 0, 0, 0
				next.Links = append(next.Links, old)
			}
		}
	}
	s.CollectorID, s.Sequence = next.ID, next.Sequence
	if err = agentproto.ValidateNetwork(s); err == nil {
		err = persist(c.path, next)
	}
	if err != nil {
		s.Status, s.Error, s.Interfaces = "error", "无法保存有效的网卡采集结果", []agentproto.NetworkInterface{}
		return s
	}
	c.state = next
	c.invalid, c.resetPending = nil, false
	sort.Slice(s.Interfaces, func(i, j int) bool { return s.Interfaces[i].Index < s.Interfaces[j].Index })
	return s
}

var errUnsupported = errors.New("network inventory unsupported")
