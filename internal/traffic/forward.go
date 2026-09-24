package traffic

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/store"
)

// Forward totals use their own subject and epoch keys. They never become share
// deltas or an extra addition to the server's separately measured NIC quota.
func ingestForwardCounters(ctx context.Context, tx *sql.Tx, serverID int64, counters []agentproto.ForwardCounter,
	delta func(string, string, int64, int64, bool) (int64, int64, bool, error),
	add func(string, int64, int64, int64) error,
) error {
	for _, counter := range counters {
		var owner int64
		err := tx.QueryRowContext(ctx, `SELECT server_id FROM port_forwards WHERE id=?`, counter.ForwardID).Scan(&owner)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && owner != serverID) {
			return errors.New("unknown or foreign forward counter identity")
		}
		if err != nil {
			return err
		}
		key := fmt.Sprintf("forward:%d:nft-forward-v1:%s", counter.ForwardID, counter.Epoch)
		rx, txBytes, ok, err := delta(key, counter.Epoch, counter.Rx, counter.Tx, true)
		if err != nil {
			return err
		}
		if ok && (rx != 0 || txBytes != 0) {
			if err := add(store.SubjectForward, counter.ForwardID, rx, txBytes); err != nil {
				return err
			}
		}
	}
	return nil
}
