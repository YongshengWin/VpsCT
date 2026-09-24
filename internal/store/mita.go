package store

import "context"

func (s *Store) MitaVersion(ctx context.Context, serverID int64) (int, error) {
	var v int
	err := s.db.QueryRowContext(ctx, `SELECT version FROM server_mita_requirements WHERE server_id=?`, serverID).Scan(&v)
	if isNoRows(err) {
		return 0, nil
	}
	return v, err
}
