package networkguard

import (
	"context"
	"errors"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/netinventory"
)

// Monitor is independent of controller HTTP and the agent's desired-state
// polling. Losing either observation or enforcement ends renewal, leaving the
// kernel lease to expire even if the caller cannot stop a proxy process.
type Monitor struct {
	Collect func() *agentproto.NetworkSnapshot
	Renew   func(context.Context, *agentproto.NetworkSnapshot) error
	Revoke  func(context.Context, map[int]bool, bool) error
	Watch   func(context.Context) (<-chan netinventory.Change, error)
}

func (m Monitor) Run(ctx context.Context) error {
	if m.Collect == nil || m.Renew == nil || m.Revoke == nil {
		return errors.New("incomplete network monitor")
	}
	watch := m.Watch
	if watch == nil {
		watch = netinventory.Watch
	}
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	// Revoke promptly when returning; leases also expire if this process is
	// killed before the deferred action executes.
	defer func() {
		safe, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = m.Revoke(safe, nil, true)
	}()
	events, err := watch(child)
	if err != nil {
		return err
	}
	if events == nil {
		return errors.New("network event stream unavailable")
	}
	renew := func() error {
		snapshot := m.Collect()
		// Drain events received during collection before using the snapshot.
		// A concurrent delete/address update invalidates that sample entirely.
		changed := false
		for budget := 0; budget < 16; budget++ {
			if child.Err() != nil {
				return child.Err()
			}
			select {
			case c, ok := <-events:
				if !ok {
					return errors.New("network event monitor stopped")
				}
				changed = true
				call, cancel := context.WithTimeout(child, time.Second)
				err := m.Revoke(call, c.Indices, c.Lost)
				cancel()
				if err != nil {
					return err
				}
			default:
				if changed {
					return nil
				}
				call, cancel := context.WithTimeout(child, time.Second)
				defer cancel()
				return m.Renew(call, snapshot)
			}
		}
		return nil
	}
	if err := renew(); err != nil {
		return err
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case c, ok := <-events:
			if !ok {
				return errors.New("network event monitor stopped")
			}
			call, cancel := context.WithTimeout(child, time.Second)
			err := m.Revoke(call, c.Indices, c.Lost)
			cancel()
			if err != nil {
				return err
			}
			if err := renew(); err != nil {
				return err
			}
		case <-ticker.C:
			if err := renew(); err != nil {
				return err
			}
		}
	}
}
