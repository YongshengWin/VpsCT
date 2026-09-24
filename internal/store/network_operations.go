package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
)

const MaxNetworkOperations = 4096
const MaxNetworkPublishAttempts = 8

// SQL orders retry deadlines as text; unlike RFC3339Nano's optional trailing
// fraction, fixed-width UTC timestamps preserve ordering within a second.
func networkOperationTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000000Z")
}

var ErrNetworkOperationConflict = errors.New("操作编号已用于不同的网络请求")
var ErrNetworkMaintenance = errors.New("服务器正在维护，暂不能修改网络配置")
var ErrNetworkOperationCapacity = errors.New("服务器网络操作记录已达上限")
var ErrNetworkRetryConflict = errors.New("操作状态已变化或不能重试，请刷新后确认")

type NetworkOperation struct {
	ID               string    `json:"id"`
	ServerID         int64     `json:"server_id"`
	Kind             string    `json:"kind"`
	ResourceID       int64     `json:"resource_id"`
	ResourceRevision int64     `json:"resource_revision"`
	Generation       int64     `json:"generation"`
	RequestHash      string    `json:"-"`
	Status           string    `json:"status"`
	DesiredRevision  int64     `json:"desired_revision"`
	DesiredHash      string    `json:"-"`
	Attempts         int       `json:"attempts"`
	RetryRevision    int64     `json:"retry_revision"`
	NextAttemptAt    time.Time `json:"next_attempt_at"`
	Message          string    `json:"message"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

const networkOperationCols = `id,server_id,kind,resource_id,resource_revision,generation,request_hash,status,desired_revision,desired_hash,attempts,retry_revision,next_attempt_at,message,created_at,updated_at`

func scanNetworkOperation(row interface{ Scan(...any) error }) (NetworkOperation, error) {
	var op NetworkOperation
	var next, created, updated string
	err := row.Scan(&op.ID, &op.ServerID, &op.Kind, &op.ResourceID, &op.ResourceRevision, &op.Generation, &op.RequestHash, &op.Status, &op.DesiredRevision, &op.DesiredHash, &op.Attempts, &op.RetryRevision, &next, &op.Message, &created, &updated)
	if isNoRows(err) {
		err = ErrNotFound
	}
	op.NextAttemptAt, op.CreatedAt, op.UpdatedAt = parseTime(next), parseTime(created), parseTime(updated)
	return op, err
}

func networkGeneration(ctx context.Context, q querier, serverID int64) (int64, error) {
	var generation int64
	err := q.QueryRowContext(ctx, `SELECT generation FROM server_network_generations WHERE server_id=?`, serverID).Scan(&generation)
	if isNoRows(err) {
		return 0, nil
	}
	return generation, err
}

func (s *Store) NetworkGeneration(ctx context.Context, serverID int64) (int64, error) {
	return networkGeneration(ctx, s.db, serverID)
}

func networkMaintenance(ctx context.Context, q querier, serverID int64) error {
	var active bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM maintenance_jobs WHERE server_id=? AND status IN ('queued','running'))`, serverID).Scan(&active); err != nil {
		return err
	}
	if active {
		return ErrNetworkMaintenance
	}
	return nil
}

func (s *Store) RequireNoNetworkMaintenance(ctx context.Context, serverID int64) error {
	return networkMaintenance(ctx, s.db, serverID)
}

type NodeNetworkRequest struct {
	ID               string              `json:"operation_id"`
	NodeID           int64               `json:"node_id"`
	ExpectedRevision int64               `json:"expected_revision"`
	Network          *networkconfig.Node `json:"network"`
	AdvertiseHost    *string             `json:"advertise_host,omitempty"`
	ExpectedImpact   string              `json:"expected_impact,omitempty"`
}

func (in NodeNetworkRequest) fingerprint() (string, error) {
	if in.ExpectedImpact != "" && !validNetworkImpactToken(in.ExpectedImpact) {
		return "", ErrNetworkImpactRequired
	}
	if !networkconfig.ValidIdentity(in.ID) || in.NodeID < 1 || in.NodeID > 1<<53-1 || in.ExpectedRevision < 0 || in.ExpectedRevision >= 1<<53-1 {
		return "", errors.New("网络操作编号或版本无效")
	}
	if in.Network != nil {
		if err := in.Network.Validate(); err != nil {
			return "", err
		}
	}
	if in.AdvertiseHost != nil {
		if err := networkconfig.ValidateAdvertiseHost(*in.AdvertiseHost); err != nil {
			return "", err
		}
	}
	b, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// ExistingNodeNetworkRequest allows an exact retry to return its original
// operation even if the agent has since gone offline or the node was deleted.
func (s *Store) ExistingNodeNetworkRequest(ctx context.Context, in NodeNetworkRequest) (NetworkOperation, error) {
	hash, err := in.fingerprint()
	if err != nil {
		return NetworkOperation{}, err
	}
	op, err := s.NetworkOperation(ctx, in.ID)
	if err == nil && (op.Kind != "node_network" || op.RequestHash != hash) {
		err = ErrNetworkOperationConflict
	}
	return op, err
}

// RequestNodeNetwork persists intent, the edit and its audit together. It never
// claims that saving means the process applied the new configuration.
func (s *Store) RequestNodeNetwork(ctx context.Context, in NodeNetworkRequest, audit domain.AuditEvent) (NetworkOperation, error) {
	return s.requestNodeNetwork(ctx, in, audit, false, false)
}

// RequestReadyNodeNetwork is the management admission path: readiness and the
// mutation share one database transaction. Exact retries return their existing
// operation before current observations can invalidate a previously saved edit.
func (s *Store) RequestReadyNodeNetwork(ctx context.Context, in NodeNetworkRequest, audit domain.AuditEvent) (NetworkOperation, error) {
	return s.requestNodeNetwork(ctx, in, audit, true, false)
}

func (s *Store) RequestReviewedNodeNetwork(ctx context.Context, in NodeNetworkRequest, audit domain.AuditEvent) (NetworkOperation, error) {
	return s.requestNodeNetwork(ctx, in, audit, true, true)
}

func (s *Store) requestNodeNetwork(ctx context.Context, in NodeNetworkRequest, audit domain.AuditEvent, requireReady, requireReview bool) (NetworkOperation, error) {
	hash, err := in.fingerprint()
	if err != nil {
		return NetworkOperation{}, err
	}
	var op NetworkOperation
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		previous, err := scanNetworkOperation(tx.QueryRowContext(ctx, `SELECT `+networkOperationCols+` FROM network_operations WHERE id=?`, in.ID))
		if err == nil {
			if previous.Kind != "node_network" || previous.RequestHash != hash {
				return ErrNetworkOperationConflict
			}
			op = previous
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		n, err := s.scanNode(tx.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE id=?`, in.NodeID))
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if n.Source != domain.NodeDeployed || n.ServerID == nil {
			return errors.New("只有受管部署节点可以设置服务器网络")
		}
		if requireReview && in.ExpectedImpact == "" {
			return ErrNetworkImpactRequired
		}
		if in.ExpectedImpact != "" {
			impact, err := s.nodeNetworkImpact(ctx, tx, n, in)
			if err != nil {
				return err
			}
			if impact.Token != in.ExpectedImpact {
				return ErrNetworkImpactChanged
			}
		}
		if requireReady {
			readiness, err := s.previewNodeNetwork(ctx, tx, n, in.Network)
			if err != nil {
				return err
			}
			if !readiness.Ready {
				return ErrNetworkNotReady
			}
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM maintenance_jobs WHERE server_id=? AND status IN ('queued','running')`, *n.ServerID).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return ErrNetworkMaintenance
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM network_operations WHERE server_id=?`, *n.ServerID).Scan(&count); err != nil {
			return err
		}
		if count >= MaxNetworkOperations {
			return ErrNetworkOperationCapacity
		}
		n, err = s.setNodeNetwork(ctx, tx, in.NodeID, in.ExpectedRevision, in.Network, in.AdvertiseHost)
		if err != nil {
			return err
		}
		generation, err := networkGeneration(ctx, tx, *n.ServerID)
		if err != nil {
			return err
		}
		now := s.Now()
		op = NetworkOperation{ID: in.ID, ServerID: *n.ServerID, Kind: "node_network", ResourceID: n.ID, ResourceRevision: n.NetworkRevision, Generation: generation, RequestHash: hash,
			Status: "queued", Message: "配置已保存，等待发布到 agent", NextAttemptAt: now, CreatedAt: now, UpdatedAt: now}
		_, err = tx.ExecContext(ctx, `INSERT INTO network_operations(id,server_id,kind,resource_id,resource_revision,generation,request_hash,status,next_attempt_at,message,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, op.ID, op.ServerID, op.Kind, op.ResourceID, op.ResourceRevision, op.Generation, hash, op.Status, networkOperationTime(now), op.Message, networkOperationTime(now), networkOperationTime(now))
		if err != nil {
			return err
		}
		audit.Action, audit.Target = "node.network.request", fmt.Sprint(n.ID)
		audit.Detail, _ = json.Marshal(map[string]any{"operation_id": op.ID, "node_id": n.ID, "network_revision": n.NetworkRevision, "generation": generation})
		return s.addAudit(ctx, tx, audit)
	})
	if err != nil {
		if strings.Contains(err.Error(), "SQLITE_BUSY") || strings.Contains(err.Error(), "database is locked") {
			err = ErrNetworkConflict
		}
		return NetworkOperation{}, err
	}
	return op, nil
}

func (s *Store) NetworkOperation(ctx context.Context, id string) (NetworkOperation, error) {
	return scanNetworkOperation(s.db.QueryRowContext(ctx, `SELECT `+networkOperationCols+` FROM network_operations WHERE id=?`, id))
}

func (s *Store) NetworkOperations(ctx context.Context, serverID int64, limit, offset int) ([]NetworkOperation, error) {
	limit = max(1, min(limit, 100))
	offset = max(0, min(offset, MaxNetworkOperations))
	rows, err := s.db.QueryContext(ctx, `SELECT `+networkOperationCols+` FROM network_operations WHERE server_id=? ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?`, serverID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []NetworkOperation{}
	for rows.Next() {
		op, err := scanNetworkOperation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

// RetryNetworkOperation records a new publication attempt without changing the
// saved policy. Its revision separates delayed workers from the fresh budget.
// The republish flag is consumed only in the desired publication transaction.
func (s *Store) RetryNetworkOperation(ctx context.Context, id string, expected int64, audit domain.AuditEvent) (NetworkOperation, error) {
	if !networkconfig.ValidIdentity(id) || expected < 0 || expected >= 1<<53-1 {
		return NetworkOperation{}, ErrNetworkRetryConflict
	}
	var op NetworkOperation
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		op, err = scanNetworkOperation(tx.QueryRowContext(ctx, `SELECT `+networkOperationCols+` FROM network_operations WHERE id=?`, id))
		if err != nil {
			return err
		}
		if op.RetryRevision != expected || (op.Status != "publish_failed" && op.Status != "apply_failed" && op.Status != "waiting_agent") {
			return ErrNetworkRetryConflict
		}
		generation, err := networkGeneration(ctx, tx, op.ServerID)
		if err != nil {
			return err
		}
		if generation != op.Generation {
			return ErrNetworkRetryConflict
		}
		var maintenance int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM maintenance_jobs WHERE server_id=? AND status IN ('queued','running')`, op.ServerID).Scan(&maintenance); err != nil {
			return err
		}
		if maintenance != 0 {
			return ErrNetworkMaintenance
		}
		op.Status, op.Message = "queued", "重试已保存，等待发布新的配置版本"
		op.RetryRevision++
		op.Attempts, op.DesiredRevision, op.DesiredHash = 0, 0, ""
		op.NextAttemptAt, op.UpdatedAt = s.Now(), s.Now()
		if _, err = tx.ExecContext(ctx, `UPDATE server_network_generations SET published_generation=0,attempts=0,retry_revision=retry_revision+1,next_attempt_at=? WHERE server_id=?`, networkOperationTime(op.NextAttemptAt), op.ServerID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE network_operations SET status=?,message=?,retry_revision=?,attempts=0,desired_revision=0,desired_hash='',republish=1,next_attempt_at=?,updated_at=? WHERE id=?`, op.Status, op.Message, op.RetryRevision, networkOperationTime(op.NextAttemptAt), networkOperationTime(op.UpdatedAt), id)
		if err != nil {
			return err
		}
		audit.Action, audit.Target = "network.operation.retry", id
		audit.Detail, _ = json.Marshal(map[string]any{"operation_id": id, "retry_revision": op.RetryRevision})
		return s.addAudit(ctx, tx, audit)
	})
	if err != nil && (strings.Contains(err.Error(), "SQLITE_BUSY") || strings.Contains(err.Error(), "database is locked")) {
		err = ErrNetworkRetryConflict
	}
	return op, err
}

// attachNetworkOperations is part of the same transaction as the desired row.
// There is no crash window in which a published revision loses its receipt key.
func (s *Store) attachNetworkOperations(ctx context.Context, q querier, ds domain.DesiredState, generation int64) error {
	if _, err := q.ExecContext(ctx, `UPDATE server_network_generations SET published_generation=? WHERE server_id=? AND generation=?`, generation, ds.ServerID, generation); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, `UPDATE network_operations SET status='waiting_agent',desired_revision=?,desired_hash=?,republish=0,message='已发布，等待 agent 应用回执',updated_at=? WHERE server_id=? AND generation=? AND (status IN ('queued','publish_failed') OR (status IN ('waiting_agent','apply_failed') AND (desired_revision<>? OR desired_hash<>?)))`, ds.Revision, ds.Hash, fmtTime(s.Now()), ds.ServerID, generation, ds.Revision, ds.Hash)
	return err
}

// An exact revision AND hash are required. Generic agent health, a larger
// revision, or a successful report for superseded intent is not completion.
func (s *Store) RecordNetworkApply(ctx context.Context, serverID, revision int64, hash, status string) error {
	if (status != "applied" && status != "failed") || len(hash) != 64 {
		return nil
	}
	phase, message := "applied", "agent 已应用该网络配置"
	if status == "failed" {
		phase, message = "apply_failed", "agent 应用失败，请查看服务器诊断；旧配置或阻断可能仍在生效"
	}
	// Forward completion also requires atomic final accounting/port cleanup.
	// A generic successful apply report cannot acknowledge those side effects.
	_, err := s.db.ExecContext(ctx, `UPDATE network_operations SET status=?,message=?,updated_at=? WHERE server_id=? AND desired_revision=? AND desired_hash=? AND status IN ('waiting_agent','apply_failed') AND generation=(SELECT generation FROM server_network_generations WHERE server_id=?) AND (?<>'applied' OR kind NOT IN ('forward_create','forward_update','forward_delete'))`, phase, message, fmtTime(s.Now()), serverID, revision, hash, serverID, status)
	return err
}

// A generation is itself a durable outbox entry. This includes mutations from
// legacy paths such as deleting a bound node, even without an operation form.
type NetworkPublication struct {
	ServerID, Generation, PublishedGeneration, RetryRevision int64
	Attempts                                                 int
	NextAttemptAt                                            time.Time
}

func (s *Store) PendingNetworkPublications(ctx context.Context) ([]NetworkPublication, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT server_id,generation,published_generation,retry_revision,attempts,next_attempt_at FROM server_network_generations WHERE generation<>published_generation AND attempts<? AND next_attempt_at<=? AND NOT EXISTS(SELECT 1 FROM maintenance_jobs WHERE maintenance_jobs.server_id=server_network_generations.server_id AND status IN ('queued','running')) ORDER BY next_attempt_at,server_id LIMIT 64`, MaxNetworkPublishAttempts, networkOperationTime(s.Now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NetworkPublication{}
	for rows.Next() {
		var op NetworkPublication
		var next string
		if err := rows.Scan(&op.ServerID, &op.Generation, &op.PublishedGeneration, &op.RetryRevision, &op.Attempts, &next); err != nil {
			return nil, err
		}
		op.NextAttemptAt = parseTime(next)
		out = append(out, op)
	}
	return out, rows.Err()
}

func (s *Store) NetworkPublicationFailed(ctx context.Context, op NetworkPublication) error {
	now := s.Now()
	next := now.Add(time.Second*time.Duration(1<<min(op.Attempts+1, 5)) + time.Duration(rand.IntN(1000))*time.Millisecond)
	message := "配置发布失败，等待自动重试"
	if op.Attempts+1 >= MaxNetworkPublishAttempts {
		message = "配置连续发布失败，请重试此操作"
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, `UPDATE server_network_generations SET attempts=attempts+1,next_attempt_at=? WHERE server_id=? AND generation=? AND published_generation<>generation AND retry_revision=? AND attempts=?`, networkOperationTime(next), op.ServerID, op.Generation, op.RetryRevision, op.Attempts)
		if err != nil {
			return err
		}
		count, err := r.RowsAffected()
		if err != nil || count == 0 {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE network_operations SET status='publish_failed',attempts=?,next_attempt_at=?,message=?,updated_at=? WHERE server_id=? AND generation=? AND status IN ('queued','publish_failed')`, op.Attempts+1, networkOperationTime(next), message, networkOperationTime(now), op.ServerID, op.Generation)
		return err
	})
}
