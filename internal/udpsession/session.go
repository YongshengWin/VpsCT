// Package udpsession closes only fixed-forward UDP connections whose kernel
// conntrack admission has expired. Traffic, including empty datagrams and
// downstream-only traffic, is timed by the existing nft conntrack policy.
package udpsession

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"ctlvps/internal/agentbudget"
)

const MaxSessions = agentbudget.ForwardUDPSessions
const MaxExpiryBatch = 512

type Flow struct {
	ForwardID  int64
	Source     netip.AddrPort
	ListenPort uint16
}

type Policy struct {
	ListenPort uint16
	Idle       time.Duration
}
type Config struct {
	Secret      string
	Policies    map[int64]Policy
	ListenPorts map[int]bool
}

func ParseConfig(raw []byte) (Config, error) {
	var v struct {
		Experimental struct {
			ClashAPI struct {
				ExternalController string `json:"external_controller"`
				Secret             string `json:"secret"`
			} `json:"clash_api"`
		} `json:"experimental"`
		Inbounds []struct {
			Tag     string          `json:"tag"`
			Network json.RawMessage `json:"network"`
			Port    int             `json:"listen_port"`
			Timeout string          `json:"udp_timeout"`
		} `json:"inbounds"`
	}
	c := Config{Policies: map[int64]Policy{}, ListenPorts: map[int]bool{}}
	if len(raw) > 2<<20 || json.Unmarshal(raw, &v) != nil {
		return c, errors.New("invalid local UDP configuration")
	}
	for _, in := range v.Inbounds {
		// A UDP-only listener can share its number with the ephemeral TCP API.
		if string(in.Network) != `"udp"` {
			c.ListenPorts[in.Port] = true
		}
		if !strings.HasPrefix(in.Tag, "forward-") || string(in.Network) == `"tcp"` {
			continue
		}
		id, err := strconv.ParseInt(strings.TrimPrefix(in.Tag, "forward-"), 10, 64)
		idle, e := time.ParseDuration(in.Timeout)
		if err != nil || e != nil || id < 1 || id > 0xffffff || in.Port < 1 || in.Port > 65535 || idle < 2*time.Second || idle > 600*time.Second {
			return c, errors.New("invalid local UDP policy")
		}
		if _, exists := c.Policies[id]; exists {
			return c, errors.New("duplicate local UDP policy")
		}
		c.Policies[id] = Policy{uint16(in.Port), idle}
	}
	if len(c.Policies) == 0 {
		return c, nil
	}
	c.Secret = v.Experimental.ClashAPI.Secret
	if v.Experimental.ClashAPI.ExternalController != "127.0.0.1:0" || len(c.Secret) != 64 {
		return c, errors.New("UDP connection manager missing local authentication")
	}
	for _, r := range c.Secret {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return c, errors.New("invalid local UDP authentication")
		}
	}
	return c, nil
}

type Connection struct {
	ID       string `json:"id"`
	Metadata struct {
		Network    string `json:"network"`
		Type       string `json:"type"`
		SourceIP   string `json:"sourceIP"`
		SourcePort string `json:"sourcePort"`
	} `json:"metadata"`
	Upload   int64 `json:"upload"`
	Download int64 `json:"download"`
}

func validID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, r := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
			continue
		}
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

func (c Connection) flow(policies map[int64]Policy) (Flow, bool, error) {
	if c.Metadata.Network != "udp" || !strings.HasPrefix(c.Metadata.Type, "direct/forward-") {
		return Flow{}, false, nil
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(c.Metadata.Type, "direct/forward-"), 10, 64)
	policy, ok := policies[id]
	if !ok {
		return Flow{}, false, nil
	}
	ip, e := netip.ParseAddr(c.Metadata.SourceIP)
	port, pe := strconv.ParseUint(c.Metadata.SourcePort, 10, 16)
	if err != nil || e != nil || pe != nil || !validID(c.ID) || c.Upload < 0 || c.Download < 0 {
		return Flow{}, false, errors.New("invalid UDP connection observation")
	}
	return Flow{id, netip.AddrPortFrom(ip.Unmap(), uint16(port)), policy.ListenPort}, true, nil
}

type API struct {
	Address, Secret string
	Client          *http.Client
}

func NewAPI(address, secret string) (*API, error) {
	host, port, err := net.SplitHostPort(address)
	p, pe := strconv.ParseUint(port, 10, 16)
	if err != nil || pe != nil || host != "127.0.0.1" || p == 0 || len(secret) != 64 {
		return nil, errors.New("UDP manager must use an authenticated loopback listener")
	}
	return &API{address, secret, &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (a *API) request(ctx context.Context, method, path string) (*http.Response, error) {
	r, err := http.NewRequestWithContext(ctx, method, "http://"+a.Address+path, nil)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+a.Secret)
	resp, err := a.Client.Do(r)
	if err != nil {
		return nil, errors.New("local UDP manager unavailable")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, errors.New("local UDP manager rejected request")
	}
	return resp, nil
}

// Stream the snapshot rather than materializing other proxy nodes' sessions.
func (a *API) Connections(ctx context.Context, policies map[int64]Policy) ([]Connection, error) {
	resp, err := a.request(ctx, http.MethodGet, "/connections")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	limit := &io.LimitedReader{R: resp.Body, N: 16 << 20}
	d := json.NewDecoder(limit)
	t, err := d.Token()
	if err != nil || t != json.Delim('{') {
		return nil, errors.New("invalid local connection snapshot")
	}
	var result []Connection
	found := false
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return nil, err
		}
		if key != "connections" {
			var discard json.RawMessage
			if err = d.Decode(&discard); err != nil {
				return nil, err
			}
			continue
		}
		if found {
			return nil, errors.New("duplicate connection snapshot")
		}
		found = true
		t, err = d.Token()
		if err != nil {
			return nil, err
		}
		if t == nil {
			continue
		}
		if t != json.Delim('[') {
			return nil, errors.New("invalid local connection collection")
		}
		for d.More() {
			var c Connection
			if err = d.Decode(&c); err != nil {
				return nil, errors.New("invalid local connection observation")
			}
			_, ok, err := c.flow(policies)
			if err != nil {
				return nil, err
			}
			if ok {
				result = append(result, c)
				if len(result) > MaxSessions {
					return nil, errors.New("UDP session observation exceeds budget")
				}
			}
		}
		if _, err = d.Token(); err != nil {
			return nil, err
		}
	}
	if _, err = d.Token(); err != nil {
		return nil, err
	}
	var extra any
	if !found || limit.N <= 0 || d.Decode(&extra) != io.EOF {
		return nil, errors.New("incomplete local connection snapshot")
	}
	return result, nil
}

func (a *API) Close(ctx context.Context, id string) error {
	if !validID(id) {
		return errors.New("invalid connection identity")
	}
	resp, err := a.request(ctx, http.MethodDelete, "/connections/"+id)
	if err == nil {
		resp.Body.Close()
	}
	return err
}

type absent struct {
	since            time.Time
	upload, download int64
}
type Reaper struct {
	flows    map[Flow]bool
	Active   map[int64]int
	Reaped   uint64
	missing  map[string]absent
	activity map[string]absent
}

// Require two separated observations without conntrack admission. Counters
// changing between them postpone deletion, closing the dump/snapshot race.
func (r *Reaper) Sweep(ctx context.Context, a *API, cfg Config, live map[Flow]bool, now time.Time) error {
	connections, err := a.Connections(ctx, cfg.Policies)
	if err != nil {
		return err
	}
	next := map[string]absent{}
	active := map[int64]int{}
	flows := map[Flow]bool{}
	var expired []Connection
	activity := map[string]absent{}
	seen := map[string]bool{}
	for _, c := range connections {
		if seen[c.ID] {
			return errors.New("duplicate UDP connection identity")
		}
		seen[c.ID] = true
	}
	for _, c := range connections {
		flow, _, err := c.flow(cfg.Policies)
		if err != nil {
			return err
		}
		last, exists := r.activity[c.ID]
		if !exists || last.upload != c.Upload || last.download != c.Download {
			last = absent{now, c.Upload, c.Download}
		}
		activity[c.ID] = last
		active[flow.ForwardID]++
		flows[flow] = true
		if live[flow] {
			continue
		}
		old, exists := r.missing[c.ID]
		if exists && old.upload == c.Upload && old.download == c.Download && now.Sub(old.since) >= 500*time.Millisecond && now.Sub(last.since) >= cfg.Policies[flow.ForwardID].Idle {
			if len(expired) < MaxExpiryBatch {
				expired = append(expired, c)
			} else {
				next[c.ID] = old
			}
			continue
		}
		next[c.ID] = absent{now, c.Upload, c.Download}
		if exists && old.upload == c.Upload && old.download == c.Download {
			next[c.ID] = old
		}
	}
	closed, err := a.closeConnections(ctx, expired)
	r.Reaped += uint64(len(closed))
	for _, c := range closed {
		flow, _, _ := c.flow(cfg.Policies)
		active[flow.ForwardID]--
	}
	r.Active = active
	r.flows = flows
	if err != nil {
		return err
	}
	r.missing = next
	r.activity = activity
	return nil
}

// Limit in-flight DELETE requests so a large expiry batch cannot serialize
// thousands of round trips and starve the six-second kernel health lease.
func (a *API) closeConnections(ctx context.Context, items []Connection) ([]Connection, error) {
	var mu sync.Mutex
	var closed []Connection
	var first error
	jobs := make(chan Connection)
	var wg sync.WaitGroup
	for i := 0; i < min(8, len(items)); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range jobs {
				err := a.Close(ctx, c.ID)
				mu.Lock()
				if err == nil {
					closed = append(closed, c)
				} else if first == nil {
					first = err
				}
				mu.Unlock()
			}
		}()
	}
	for _, c := range items {
		if ctx.Err() != nil {
			break
		}
		jobs <- c
	}
	close(jobs)
	wg.Wait()
	if ctx.Err() != nil {
		return closed, ctx.Err()
	}
	return closed, first
}

// Reset closes only the scoped forward UDP snapshot before reopening an expired
// health lease. Proxy node UDP and TCP connections never enter this collection.
func (r *Reaper) Reset(ctx context.Context, a *API, cfg Config) error {
	items, err := a.Connections(ctx, cfg.Policies)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, c := range items {
		if seen[c.ID] {
			return errors.New("duplicate UDP connection identity")
		}
		seen[c.ID] = true
	}
	closed, err := a.closeConnections(ctx, items)
	r.Reaped += uint64(len(closed))
	if err != nil {
		return err
	}
	r.missing, r.activity, r.Active, r.flows = nil, nil, nil, nil
	return nil
}

// ObservedFlows is a read-only hint from the last complete API snapshot. New
// connections receive a fresh idle observation before they can be reaped.
func (r *Reaper) ObservedFlows() map[Flow]bool { return r.flows }
