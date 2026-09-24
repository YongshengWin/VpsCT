package traffic

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/store"
)

func (i *Ingestor) ingestNetworkBilling(ctx context.Context, tx *sql.Tx, serverID int64, hb agentproto.Heartbeat, ts time.Time, result *Result, legacy func() error, add func(string, int64, int64, int64) error, sample func(any, int64, int64) error) error {
	var current, requested, totalRx, totalTx int64
	var source string
	var previousStatus, previousError string
	var sequence int64
	err := tx.QueryRowContext(ctx, "SELECT current_revision,requested_revision,rx_total,tx_total,snapshot_source,snapshot_sequence,status,error FROM network_billing_state WHERE server_id=?", serverID).Scan(&current, &requested, &totalRx, &totalTx, &source, &sequence, &previousStatus, &previousError)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	policy, err := store.BillingPolicy(ctx, tx, serverID, current)
	if err != nil {
		return err
	}
	// Keep a durable, bounded-by-audit-retention record of missing intervals and
	// recovery. A later healthy heartbeat must not erase evidence of a gap.
	health := func(status, detail string) error {
		if !exists || (previousStatus == status && previousError == detail) {
			return nil
		}
		revision := current
		if hb.NetworkBillingSwitch != nil {
			revision = hb.NetworkBillingSwitch.Next.Revision
		}
		payload, err := json.Marshal(map[string]any{"server_id": serverID, "revision": revision, "status": status, "error": detail, "sampled_at": ts})
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO audit_log(ts,action,target,detail) VALUES (?,?,?,?)", i.Now().UTC().Format(time.RFC3339Nano), "server.network_billing.health", fmt.Sprint(serverID), string(payload))
		return err
	}
	var hash string
	if sw := hb.NetworkBillingSwitch; sw != nil {
		payload, _ := json.Marshal(sw)
		sum := sha256.Sum256(payload)
		hash = hex.EncodeToString(sum[:])
		var previous string
		err = tx.QueryRowContext(ctx, "SELECT payload_hash FROM network_billing_settlements WHERE server_id=? AND batch_id=?", serverID, sw.ID).Scan(&previous)
		if err == nil {
			if previous != hash {
				return errors.New("billing settlement ID reused with different content")
			}
			result.NetworkBillingAck = sw.ID
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if !exists || sw.PreviousRevision != current || sw.Next.Revision > requested {
			return errors.New("billing transition does not match current policy")
		}
		next, err := store.BillingPolicy(ctx, tx, serverID, sw.Next.Revision)
		if err != nil {
			return err
		}
		if !next.Equal(sw.Next) {
			return errors.New("billing transition changed policy")
		}
	} else if hb.NetworkBillingHeld {
		if !exists {
			return nil
		}
		detail := "等待持久化切换快照确认；节点计量继续"
		if err = health("switching", detail); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE network_billing_state SET status='switching',error=? WHERE server_id=?", detail, serverID)
		return err
	} else if current > 0 && hb.NetworkBillingRevision != current {
		detail := "agent 尚未确认当前计费策略；未回退旧来源"
		if err = health("incomplete", detail); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE network_billing_state SET status='incomplete',error=? WHERE server_id=?", detail, serverID)
		return err // Node/share meters still continue in the caller.
	}
	if snapshot := hb.Metrics.Network; exists && snapshot != nil && (snapshot.Status == "ok" || snapshot.Status == "incomplete") {
		if err := agentproto.ValidateNetwork(snapshot); err != nil {
			return err
		}
		var previous int64
		err = tx.QueryRowContext(ctx, "SELECT sequence FROM network_billing_sources WHERE server_id=? AND collector_id=?", serverID, snapshot.CollectorID).Scan(&previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		retired := err == nil && strings.SplitN(source, ":", 2)[0] != snapshot.CollectorID
		stale := err == nil && snapshot.Sequence <= previous
		if retired || stale {
			if hb.NetworkBillingSwitch != nil {
				if retired || snapshot.Sequence < previous {
					return errors.New("billing switch conflicts with newer sampling source")
				}
			} else if policy.Mode == "interfaces" {
				return nil
			}
		}
		if !retired && !stale {
			var count int
			if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM network_billing_sources WHERE server_id=?", serverID).Scan(&count); err != nil {
				return err
			}
			if previous == 0 && count >= 4096 {
				return errors.New("billing collector registry capacity exceeded")
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO network_billing_sources(server_id,collector_id,sequence) VALUES (?,?,?) ON CONFLICT(server_id,collector_id) DO UPDATE SET sequence=excluded.sequence`, serverID, snapshot.CollectorID, snapshot.Sequence); err != nil {
				return err
			}
			source, sequence = snapshot.CollectorID+":"+snapshot.BootID, snapshot.Sequence
		}
	}
	status, detail := "active", ""
	if policy.Mode == "legacy" {
		if err = legacy(); err != nil {
			return err
		}
		if current == 0 {
			totalRx, totalTx = hb.Metrics.NetRx, hb.Metrics.NetTx
		} else {
			if result.ServerUp > math.MaxInt64-totalRx || result.ServerDown > math.MaxInt64-totalTx {
				return errors.New("server billing totals overflow")
			}
			totalRx += result.ServerUp
			totalTx += result.ServerDown
		}
		if hb.Metrics.NetRx > 0 || hb.Metrics.NetTx > 0 {
			if err = sample(nil, totalRx, totalTx); err != nil {
				return err
			}
		}
		if current == 0 {
			status = "legacy"
		}
	} else {
		var rx, out int64
		var missing int
		if topologyErr := agentproto.ValidateBillingTopology(hb.Metrics.Network, policy); topologyErr != nil {
			missing = len(policy.InterfaceIDs)
			detail = topologyErr.Error()
			_, err = tx.ExecContext(ctx, "DELETE FROM network_billing_baselines WHERE server_id=?", serverID)
		} else {
			rx, out, missing, err = billingInterfaceDelta(ctx, tx, serverID, policy, hb.Metrics.Network, false)
		}
		if err != nil {
			return err
		}
		result.ServerUp, result.ServerDown = rx, out
		if missing > 0 {
			status = "incomplete"
			if detail == "" {
				detail = fmt.Sprintf("%d 张计费网卡缺少连续有效采样；仅记录可确认增量", missing)
			}
		}
		if err = add(store.SubjectServer, serverID, rx, out); err != nil {
			return err
		}
		if rx > math.MaxInt64-totalRx || out > math.MaxInt64-totalTx {
			return errors.New("server billing totals overflow")
		}
		totalRx += rx
		totalTx += out
		if missing < len(policy.InterfaceIDs) {
			if err = sample(nil, totalRx, totalTx); err != nil {
				return err
			}
		}
	}
	if sw := hb.NetworkBillingSwitch; sw != nil {
		// Settle the old set and baseline the new set in the same transaction.
		if _, err = tx.ExecContext(ctx, "DELETE FROM network_billing_baselines WHERE server_id=?", serverID); err != nil {
			return err
		}
		if sw.Next.Mode == "interfaces" {
			_, _, missing, err := billingInterfaceDelta(ctx, tx, serverID, sw.Next, sw.Snapshot, true)
			if err != nil {
				return err
			}
			if missing != 0 {
				return errors.New("new billing baseline incomplete")
			}
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO counter_state(server_id,counter_key,epoch,last_rx,last_tx,updated_at) VALUES (?,'nic',?,?,?,?) ON CONFLICT(server_id,counter_key) DO UPDATE SET epoch=excluded.epoch,last_rx=excluded.last_rx,last_tx=excluded.last_tx,updated_at=excluded.updated_at`, serverID, sw.Legacy.Epoch, sw.Legacy.Rx, sw.Legacy.Tx, ts.UTC().Format(time.RFC3339Nano))
			if err != nil {
				return err
			}
		}
		if detail == "" {
			status = "active"
		}
		_, err = tx.ExecContext(ctx, "UPDATE network_billing_state SET current_revision=?,applied_at=? WHERE server_id=?", sw.Next.Revision, ts.UTC().Format(time.RFC3339Nano), serverID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO network_billing_settlements(server_id,batch_id,payload_hash,created_at) VALUES (?,?,?,?)", serverID, sw.ID, hash, i.Now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
		result.NetworkBillingAck = sw.ID
	}
	if exists {
		if err = health(status, detail); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE network_billing_state SET rx_total=?,tx_total=?,status=?,error=?,sampled_at=?,snapshot_source=?,snapshot_sequence=? WHERE server_id=?", totalRx, totalTx, status, detail, ts.UTC().Format(time.RFC3339Nano), source, sequence, serverID)
	}
	return err
}

func billingInterfaceDelta(ctx context.Context, tx *sql.Tx, serverID int64, policy agentproto.NetworkBillingPolicy, snapshot *agentproto.NetworkSnapshot, baseline bool) (rx, out int64, missing int, err error) {
	if snapshot == nil || (snapshot.Status != "ok" && snapshot.Status != "incomplete") {
		return 0, 0, len(policy.InterfaceIDs), nil
	}
	if err = agentproto.ValidateNetwork(snapshot); err != nil {
		return
	}
	byID := map[string]agentproto.NetworkInterface{}
	for _, n := range snapshot.Interfaces {
		byID[n.ID] = n
	}
	source := snapshot.CollectorID + ":" + snapshot.BootID
	for _, id := range policy.InterfaceIDs {
		n, ok := byID[id]
		if !ok || !n.CountersValid {
			missing++
			continue
		}
		var previous, generation string
		var sequence, oldRx, oldTx int64
		err = tx.QueryRowContext(ctx, "SELECT source,generation,sequence,rx,tx FROM network_billing_baselines WHERE server_id=? AND interface_id=?", serverID, id).Scan(&previous, &generation, &sequence, &oldRx, &oldTx)
		found := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return
		}
		if found && strings.SplitN(previous, ":", 2)[0] == snapshot.CollectorID && snapshot.Sequence <= sequence {
			continue
		}
		continuous := found && previous == source && generation == n.Generation && n.Rx >= oldRx && n.Tx >= oldTx
		if !baseline && !continuous {
			missing++
		}
		if !baseline && continuous {
			if n.Rx-oldRx > math.MaxInt64-rx || n.Tx-oldTx > math.MaxInt64-out {
				return 0, 0, 0, errors.New("billing delta overflow")
			}
			rx += n.Rx - oldRx
			out += n.Tx - oldTx
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO network_billing_baselines(server_id,interface_id,source,generation,sequence,rx,tx) VALUES (?,?,?,?,?,?,?) ON CONFLICT(server_id,interface_id) DO UPDATE SET source=excluded.source,generation=excluded.generation,sequence=excluded.sequence,rx=excluded.rx,tx=excluded.tx`, serverID, id, source, n.Generation, snapshot.Sequence, n.Rx, n.Tx)
		if err != nil {
			return
		}
	}
	err = nil
	return
}
