package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"ctlvps/internal/domain"
)

// The data migration runs in the same transaction as its schema-version marker.
// Do not change these historical values when future defaults are updated.
const coreVersionPinMigration = "-- freeze installation core version\nSELECT 1;"
const legacyDefaultSingBoxVersion = "1.12.14"
const initialDefaultSingBoxVersion = "1.14.1"

func (s *Store) pinInitialCoreVersion(ctx context.Context, tx *sql.Tx, fresh bool) error {
	var raw string
	err := tx.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=?", domain.SettingSingBoxVersion).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		value, err := s.decrypt("settings.value", raw)
		if err != nil {
			return err // Never turn an unreadable existing pin into a new default.
		}
		if strings.TrimSpace(value) != "" {
			return nil // Preserve explicit versions, including historical prereleases.
		}
	}
	version := legacyDefaultSingBoxVersion
	if fresh {
		version = initialDefaultSingBoxVersion
	}
	// Materializing the already-effective default is not a configuration edit.
	// Suspend only the two known generation triggers inside this transaction;
	// otherwise pending operations would be superseded by a no-op migration.
	var triggerSQL []string
	for _, name := range []string{"network_generation_setting_insert", "network_generation_setting_update"} {
		var definition string
		if err := tx.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?", name).Scan(&definition); err != nil {
			return fmt.Errorf("read setting trigger: %w", err)
		}
		triggerSQL = append(triggerSQL, definition)
		if _, err := tx.ExecContext(ctx, "DROP TRIGGER "+name); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, domain.SettingSingBoxVersion, s.seal("settings.value", version))
	if err != nil {
		return err
	}
	for _, definition := range triggerSQL {
		if _, err := tx.ExecContext(ctx, definition); err != nil {
			return err
		}
	}
	return nil
}
