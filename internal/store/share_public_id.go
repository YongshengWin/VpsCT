package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"strings"
)

const sharePublicIDSchema = `ALTER TABLE shares ADD COLUMN public_id TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX shares_public_id_unique ON shares(public_id) WHERE public_id <> '';
CREATE TRIGGER shares_public_id_required BEFORE INSERT ON shares
WHEN length(NEW.public_id) <> 34 OR substr(NEW.public_id,1,2) <> 's_' OR substr(NEW.public_id,3) GLOB '*[^0-9a-f]*'
BEGIN SELECT RAISE(ABORT,'invalid share public id'); END;
CREATE TRIGGER shares_public_id_immutable BEFORE UPDATE OF public_id ON shares
WHEN OLD.public_id <> '' AND NEW.public_id <> OLD.public_id BEGIN SELECT RAISE(ABORT,'share public id is immutable'); END;`

func newSharePublicID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return "s_" + hex.EncodeToString(bytes[:]), nil
}

func validSharePublicID(id string) bool {
	if len(id) != 34 || !strings.HasPrefix(id, "s_") {
		return false
	}
	for _, c := range id[2:] {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func backfillSharePublicIDs(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM shares WHERE public_id='' ORDER BY id`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		publicID, err := newSharePublicID()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE shares SET public_id=? WHERE id=?`, publicID, id); err != nil {
			return err
		}
	}
	return nil
}
