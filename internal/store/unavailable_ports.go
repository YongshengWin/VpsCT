package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"ctlvps/internal/domain"
)

const unavailablePortsSchema = `
DROP TABLE IF EXISTS latency_segments;
DROP TABLE IF EXISTS latency_tests;
CREATE TABLE unavailable_ports (
 server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
 port INTEGER NOT NULL CHECK(port BETWEEN 1 AND 65535),
 transport TEXT NOT NULL CHECK(transport IN ('tcp','udp','both')),
 network TEXT NOT NULL DEFAULT '', reason TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 PRIMARY KEY(server_id,port)
);`

var ErrUnavailablePort = errors.New("端口须为 1–65535，传输须为 tcp/udp/both，网络备注最多 120 字节，原因最多 500 字节")
var ErrUnavailablePortCapacity = errors.New("该服务器最多记录 512 个不可用端口，请先移除过期记录")

type UnavailablePort struct {
	ServerID  int64     `json:"server_id"`
	Port      int       `json:"port"`
	Transport string    `json:"transport"`
	Network   string    `json:"network"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func unavailablePort(ctx context.Context, q querier, serverID int64, port int) (UnavailablePort, error) {
	var p UnavailablePort
	var created, updated string
	err := q.QueryRowContext(ctx, `SELECT server_id,port,transport,network,reason,created_at,updated_at FROM unavailable_ports WHERE server_id=? AND port=?`, serverID, port).Scan(&p.ServerID, &p.Port, &p.Transport, &p.Network, &p.Reason, &created, &updated)
	if isNoRows(err) {
		return p, ErrNotFound
	}
	p.CreatedAt = parseTime(created)
	p.UpdatedAt = parseTime(updated)
	return p, err
}
func (s *Store) UnavailablePort(ctx context.Context, serverID int64, port int) (UnavailablePort, error) {
	return unavailablePort(ctx, s.db, serverID, port)
}
func (s *Store) UnavailablePorts(ctx context.Context, serverID int64) ([]UnavailablePort, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT server_id,port,transport,network,reason,created_at,updated_at FROM unavailable_ports WHERE server_id=? ORDER BY port`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UnavailablePort{}
	for rows.Next() {
		var p UnavailablePort
		var created, updated string
		if err = rows.Scan(&p.ServerID, &p.Port, &p.Transport, &p.Network, &p.Reason, &created, &updated); err != nil {
			return nil, err
		}
		p.CreatedAt = parseTime(created)
		p.UpdatedAt = parseTime(updated)
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) SaveUnavailablePort(ctx context.Context, p UnavailablePort, audit domain.AuditEvent) (UnavailablePort, error) {
	p.Network = strings.TrimSpace(p.Network)
	p.Reason = strings.TrimSpace(p.Reason)
	if p.ServerID < 1 || p.Port < 1 || p.Port > 65535 || (p.Transport != "tcp" && p.Transport != "udp" && p.Transport != "both") || len(p.Network) > 120 || len(p.Reason) > 500 || strings.ContainsRune(p.Network+p.Reason, '\x00') {
		return p, ErrUnavailablePort
	}
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM servers WHERE id=?)`, p.ServerID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM unavailable_ports WHERE server_id=? AND port<>?`, p.ServerID, p.Port).Scan(&count); err != nil {
			return err
		}
		if count >= 512 {
			return ErrUnavailablePortCapacity
		}
		now := fmtTime(s.Now())
		_, err := tx.ExecContext(ctx, `INSERT INTO unavailable_ports(server_id,port,transport,network,reason,created_at,updated_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(server_id,port) DO UPDATE SET transport=excluded.transport,network=excluded.network,reason=excluded.reason,updated_at=excluded.updated_at`, p.ServerID, p.Port, p.Transport, p.Network, p.Reason, now, now)
		if err != nil {
			return err
		}
		audit.Action = "server.port.unavailable.save"
		audit.Target = serverPortTarget(p.ServerID, p.Port)
		audit.Detail, _ = json.Marshal(map[string]any{"server_id": p.ServerID, "port": p.Port, "transport": p.Transport, "network": p.Network, "reason": p.Reason})
		return s.addAudit(ctx, tx, audit)
	})
	if err != nil {
		return p, err
	}
	return s.UnavailablePort(ctx, p.ServerID, p.Port)
}
func (s *Store) DeleteUnavailablePort(ctx context.Context, serverID int64, port int, audit domain.AuditEvent) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM servers WHERE id=?)`, serverID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM unavailable_ports WHERE server_id=? AND port=?`, serverID, port)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		audit.Action = "server.port.unavailable.remove"
		audit.Target = serverPortTarget(serverID, port)
		audit.Detail, _ = json.Marshal(map[string]any{"server_id": serverID, "port": port})
		return s.addAudit(ctx, tx, audit)
	})
}

func serverPortTarget(serverID int64, port int) string { return fmt.Sprintf("%d:%d", serverID, port) }
