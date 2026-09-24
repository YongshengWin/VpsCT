package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"ctlvps/internal/agentbudget"
	"ctlvps/internal/agentproto"
	"ctlvps/internal/agentwork"
	"ctlvps/internal/safehttp"
	"golang.org/x/sys/unix"
)

const networkRecoveryFile = "network-desired.json"

type networkDesiredCache struct {
	raw      []byte
	revision int64
	hash     string
	nodes    map[int64]bool
	forwards map[int64]bool
}

func validateNetworkRecovery(ds *agentproto.DesiredState, state *State) error {
	revision, hash := state.desiredBoundary()
	if ds == nil || (ds.NetworkEgressVersion != agentproto.NetworkEgressVersion && ds.NetworkForwardVersion != agentproto.NetworkForwardVersion) || revision == 0 || ds.Revision != revision || ds.Hash != hash {
		return errors.New("本机中转恢复配置不匹配最新已接受代次")
	}
	return agentproto.ValidateDesired(ds, state.ServerID, revision, hash)
}

func decodeNetworkRecovery(raw []byte, state *State) (*networkDesiredCache, error) {
	if len(raw) > agentbudget.ConfigBytes {
		return nil, errors.New("network recovery exceeds budget")
	}
	if err := safehttp.CheckJSONBudget(raw); err != nil {
		return nil, err
	}
	var ds agentproto.DesiredState
	if err := json.Unmarshal(raw, &ds); err != nil {
		return nil, errors.New("invalid network recovery data")
	}
	if err := validateNetworkRecovery(&ds, state); err != nil {
		return nil, err
	}
	c := &networkDesiredCache{raw: raw, revision: ds.Revision, hash: ds.Hash, nodes: map[int64]bool{}, forwards: map[int64]bool{}}
	for _, n := range ds.Nodes {
		if n.Blocked || n.Network == nil || !n.Network.HasTransport() {
			continue
		}
		// Literal endpoints also need offline recovery and reapplication when
		// the lease monitor observes a revoked local transport grant.
		c.nodes[n.NodeID] = true
	}
	for _, f := range ds.Forwards {
		if !f.Blocked && !f.Retired {
			c.forwards[f.ForwardID] = true
		}
	}
	return c, nil
}

func (a *Agent) loadNetworkDesired() error {
	path := filepath.Join(a.StateDir, networkRecoveryFile)
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var stat unix.Stat_t
	if err = unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Uid != uint32(os.Geteuid()) || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0077 != 0 {
		return errors.New("network recovery must be an owner-only regular file")
	}
	raw, err := safehttp.ReadBounded(f, agentbudget.ConfigBytes)
	if err != nil {
		return err
	}
	cache, err := decodeNetworkRecovery(raw, a.State)
	if err == nil {
		a.networkCache = cache
	}
	return err
}

func (a *Agent) saveNetworkDesired(raw []byte) error {
	cache, err := decodeNetworkRecovery(raw, a.State)
	if err != nil {
		return err
	}
	if a.networkCache != nil && bytes.Equal(a.networkCache.raw, raw) {
		return nil
	}
	if err := os.MkdirAll(a.StateDir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(a.StateDir, ".network-desired-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), filepath.Join(a.StateDir, networkRecoveryFile)); err != nil {
		return err
	}
	dir, err := os.Open(a.StateDir)
	if err != nil {
		return err
	}
	err = dir.Sync()
	dir.Close()
	if err != nil {
		return err
	}
	a.networkCache = cache
	return nil
}

// Called under stateMu; cached old intent can never become authoritative merely
// because the controller is unavailable or a newer application failed.
func (a *Agent) networkRefreshDue(now time.Time) bool {
	c := a.networkCache
	if a.bindings == nil || c == nil || (len(c.nodes) == 0 && len(c.forwards) == 0) || now.Before(a.networkRetryAt) {
		return false
	}
	revision, hash := a.State.desiredBoundary()
	if c.revision != revision || c.hash != hash {
		return false
	}
	if a.bindings.plan == nil {
		return true
	}
	health, guardError := a.bindings.status()
	if guardError != "" {
		return true
	}
	r := a.bindings
	r.mu.Lock()
	forwardBad := len(r.forwardHealth) > 0
	r.mu.Unlock()
	if forwardBad {
		return true
	}
	forwardSeen := map[int64]bool{}
	for _, b := range r.plan.Bindings {
		if c.forwards[b.ForwardID] {
			forwardSeen[b.ForwardID] = true
			if b.Pending || (b.Applied.ForwardTarget != nil && b.Applied.ForwardTarget.DNSRefreshDue(now)) {
				return true
			}
		}
	}
	if len(forwardSeen) != len(c.forwards) {
		return true
	}
	seen := map[int64]bool{}
	for _, b := range a.bindings.plan.Bindings {
		if !c.nodes[b.NodeID] {
			continue
		}
		seen[b.NodeID] = true
		if b.Pending || health[b.NodeID] != "" || b.Applied.SOCKS5 == nil || b.Applied.SOCKS5.DNSRefreshDue(now) {
			return true
		}
	}
	return len(seen) != len(c.nodes)
}

func (a *Agent) networkRefreshCandidate(now time.Time) *agentproto.DesiredState {
	if !a.networkRefreshDue(now) {
		return nil
	}
	var ds agentproto.DesiredState
	if json.Unmarshal(a.networkCache.raw, &ds) != nil || validateNetworkRecovery(&ds, a.State) != nil {
		return nil
	}
	return &ds // fresh copy; local authorization cannot mutate the recovery cache
}

func (a *Agent) networkRefreshLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			// HTTP, maintenance and core workers may temporarily own the main
			// state. The separate lease monitor still expires stale endpoints.
			if !a.stateMu.TryLock() {
				continue
			}
			if ctx.Err() == nil && !agentwork.Pending() {
				ds := a.networkRefreshCandidate(now)
				if ds != nil {
					a.networkRetryAt = now.Add(10 * time.Second)
					a.convergeDesired(ctx, true, ds)
				}
			}
			a.stateMu.Unlock()
		}
	}
}
