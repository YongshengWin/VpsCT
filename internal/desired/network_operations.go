package desired

import (
	"context"
	"errors"
	"fmt"
	"time"

	"ctlvps/internal/store"
)

// ReconcileNetworkOperations runs on startup and periodically. Database intent
// survives API request cancellation, process exit and transient build failures.
func (b *Builder) ReconcileNetworkOperations(ctx context.Context) error {
	ctx, cancelBatch := context.WithTimeout(ctx, 30*time.Second)
	defer cancelBatch()
	if err := b.reconcileTransits(ctx); err != nil {
		return err
	}
	operations, err := b.Store.PendingNetworkPublications(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, op := range operations {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, _, err := b.Publish(attempt, op.ServerID)
		cancel()
		if errors.Is(err, store.ErrNetworkMaintenance) {
			// Maintenance admitted after the pending scan parks this intent.
			// It does not consume the publication failure budget.
			continue
		}
		if err != nil {
			if saveErr := b.Store.NetworkPublicationFailed(ctx, op); saveErr != nil {
				failures = append(failures, saveErr)
			}
			// Errors may originate in configuration compilers. Keep secrets out
			// of operation status and scheduled-job diagnostics.
			failures = append(failures, fmt.Errorf("network publication failed for server %d", op.ServerID))
		}
	}
	return errors.Join(failures...)
}
