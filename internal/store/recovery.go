package store

import (
	"context"
	"ctlvps/internal/backup"
	"errors"
	"os"
	"path/filepath"
)

// BackupEntry is a terminal-only operation. Keys are supplied as file paths.
func BackupEntry(args []string) (bool, error) {
	if len(args) == 0 || args[0] != "backup" {
		return false, nil
	}
	if len(args) != 5 || (args[1] != "seal" && args[1] != "open" && args[1] != "restore") {
		return true, errors.New("usage: ctlvpsd backup seal|open|restore KEY_FILE SOURCE DESTINATION")
	}
	if _, e := os.Stat(args[2]); e != nil {
		return true, e
	}
	a, e := loadSecretKey(args[2])
	if e != nil {
		return true, e
	}
	if args[1] != "restore" {
		return true, backup.File(args[3], args[4], a, args[1] == "open")
	}
	if _, e = os.Lstat(args[4]); !os.IsNotExist(e) {
		return true, errors.New("restore requires a new destination")
	}
	dir, e := os.MkdirTemp(filepath.Dir(args[4]), ".restore-")
	if e != nil {
		return true, e
	}
	defer os.RemoveAll(dir)
	dst := filepath.Join(dir, "restore.db")
	if e = backup.File(args[3], dst, a, true); e != nil {
		return true, e
	}
	s, e := OpenWithKey(dst, args[2])
	if e != nil {
		return true, e
	}
	// Lost responses and old enrollment links must never become usable again.
	_, e = s.db.ExecContext(context.Background(), `DELETE FROM sessions; UPDATE agents SET enroll_token_hash='',enroll_expires_at=NULL; UPDATE maintenance_jobs SET status='interrupted',report_hash='',report_token='' WHERE status IN ('queued','running');`)
	if e == nil {
		_, e = s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	}
	ce := s.Close()
	if e != nil {
		return true, e
	}
	if ce != nil {
		return true, ce
	}
	return true, os.Link(dst, args[4])
}
