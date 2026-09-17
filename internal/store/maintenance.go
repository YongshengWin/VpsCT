package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"ctlvps/internal/auth"
	"ctlvps/internal/maintenance"
)

type MaintenanceJob struct {
	maintenance.Job
	DeleteServer bool   `json:"delete_server"`
	ServerID     int64  `json:"server_id"`
	ReportToken  string `json:"-"`
	ReportHash   string `json:"-"`
	AgentSHA     string `json:"-"`
}

func (s *Store) CreateMaintenance(ctx context.Context, j MaintenanceJob) error {
	r, _ := json.Marshal(j.Request)
	result, _ := json.Marshal(j.Job)
	_, err := s.db.ExecContext(ctx, `INSERT INTO maintenance_jobs(id,server_id,request,status,result,report_token,report_hash,agent_sha,created_at,updated_at,delete_server) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, j.ID, j.ServerID, string(r), j.Status, string(result), s.seal("maintenance_jobs.report_token", j.ReportToken), auth.HashToken(j.ReportToken), j.AgentSHA, fmtTime(j.CreatedAt), fmtTime(j.UpdatedAt), j.DeleteServer)
	return err
}

func (s *Store) scanMaintenance(row interface{ Scan(...any) error }) (MaintenanceJob, error) {
	var j MaintenanceJob
	var result, status string
	err := row.Scan(&j.ServerID, &result, &status, s.scanSecret("maintenance_jobs.report_token", &j.ReportToken), &j.ReportHash, &j.AgentSHA, &j.DeleteServer)
	if isNoRows(err) {
		return j, ErrNotFound
	}
	if err != nil {
		return j, err
	}
	err = json.Unmarshal([]byte(result), &j.Job)
	j.Status = status
	return j, err
}

func (s *Store) GetMaintenance(ctx context.Context, id string) (MaintenanceJob, error) {
	return s.scanMaintenance(s.db.QueryRowContext(ctx, `SELECT server_id,result,status,report_token,report_hash,agent_sha,delete_server FROM maintenance_jobs WHERE id=?`, id))
}

func (s *Store) ListMaintenance(ctx context.Context, serverID int64) ([]MaintenanceJob, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT server_id,result,status,report_token,report_hash,agent_sha,delete_server FROM maintenance_jobs WHERE server_id=? ORDER BY created_at DESC LIMIT 20`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MaintenanceJob{}
	for rows.Next() {
		j, err := s.scanMaintenance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// SaveMaintenance is a compare-and-swap: terminal reports, expiry and claims
// cannot resurrect or overwrite a job in a different state.
func (s *Store) SaveMaintenance(ctx context.Context, j MaintenanceJob, previous string) error {
	j.UpdatedAt = s.Now()
	b, _ := json.Marshal(j.Job)
	token := j.ReportToken
	if !j.Active() {
		token = ""
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, `UPDATE maintenance_jobs SET status=?,result=?,report_token=?,updated_at=? WHERE id=? AND status=?`, j.Status, string(b), s.seal("maintenance_jobs.report_token", token), fmtTime(j.UpdatedAt), j.ID, previous)
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("维护任务状态已变化")
		}
		if j.DeleteServer && j.Role == "agent" && j.Action == "uninstall" && j.Status == "succeeded" {
			return deleteServerTx(ctx, tx, j.ServerID)
		}
		return nil
	})
}

func (s *Store) ExpireMaintenance(ctx context.Context, serverID int64) error {
	jobs, err := s.ListMaintenance(ctx, serverID)
	if err != nil {
		return err
	}
	for _, j := range jobs {
		previous := j.Status
		if j.Status == "queued" && s.Now().Sub(j.CreatedAt) > 15*time.Minute {
			j.Status, j.Stage, j.Message = "expired", "expired", "任务等待超过 15 分钟，已取消执行；请确认 agent 在线后重试"
		} else if j.Status == "running" && s.Now().Sub(j.UpdatedAt) > time.Hour {
			j.Status, j.Stage, j.Message = "interrupted", "interrupted", "超过一小时未收到结果，请检查服务器上的维护日志"
		} else {
			continue
		}
		if err := s.SaveMaintenance(ctx, j, previous); err != nil {
			return err
		}
	}
	return nil
}
