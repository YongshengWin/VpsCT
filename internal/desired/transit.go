package desired

import (
	"context"
	"ctlvps/internal/store"
	"errors"
)

// Intent and stages are durable. A restart resumes at the last committed
// stage; publication alone is never interpreted as remote application.
func (b *Builder) reconcileTransits(ctx context.Context) error {
	items, err := b.Store.ManagedTransits(ctx, 0)
	if err != nil {
		return err
	}
	for _, t := range items {
		if t.Stage == "retired" || t.Stage == "applied" {
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		serverID := t.LandingServerID
		next := ""
		switch t.Stage {
		case "pending_landing":
			next = "pending_entry"
		case "pending_entry":
			serverID = t.EntryServerID
			next = "applied"
		case "stopping_entry":
			serverID = t.EntryServerID
			next = "stopping_landing"
		case "stopping_landing":
			next = "retired"
		default:
			continue
		}
		// The existing outbox owns bounded retries and maintenance parking.
		rec, e := b.Store.LatestDesiredState(ctx, serverID)
		if e != nil {
			continue
		}
		a, e := b.Store.GetAgentByServer(ctx, serverID)
		if e != nil || a.ApplyError != "" || a.AppliedRevision != rec.Revision || a.AppliedHash != rec.Hash {
			continue
		}
		if e = b.Store.AdvanceManagedTransit(ctx, t, next); e != nil && !errors.Is(e, store.ErrNetworkConflict) {
			continue
		}
	}
	return nil
}
