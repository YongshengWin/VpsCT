package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"ctlvps/internal/domain"
)

var ErrPersonalTemplateInUse = errors.New("personal template is in use")
var ErrPersonalTemplateLimit = errors.New("personal template limit reached or account unavailable")

// CreatePersonalTemplate atomically creates an encrypted template and binds it
// to an account; no unowned intermediate record is visible to other requests.
func (s *Store) CreatePersonalTemplate(ctx context.Context, ownerID int64, t *domain.RuleTemplate, limit int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if limit <= 0 {
		return errors.New("invalid template limit")
	}
	if len(t.Variables) == 0 {
		t.Variables = []byte("{}")
	}
	now := s.Now()
	res, err := tx.ExecContext(ctx, `INSERT INTO rule_templates(name,kind,description,content,variables,is_builtin,created_at,updated_at)
 SELECT ?,?,?,?,?,0,?,? WHERE EXISTS (SELECT 1 FROM users WHERE id=? AND enabled=1)
 AND (SELECT count(*) FROM personal_templates WHERE owner_id=?) < ?`,
		t.Name, t.Kind, t.Description, s.seal("rule_templates.content", t.Content), s.seal("rule_templates.variables", string(t.Variables)), fmtTime(now), fmtTime(now), ownerID, ownerID, limit)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrPersonalTemplateLimit
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO personal_templates(template_id,owner_id) VALUES(?,?)`, id, ownerID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	t.ID, t.IsBuiltin, t.CreatedAt, t.UpdatedAt = id, false, now, now
	return nil
}

// ListPersonalTemplates returns only the named account's templates.
func (s *Store) ListPersonalTemplates(ctx context.Context, ownerID int64) ([]domain.RuleTemplate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+tplCols+` FROM rule_templates
 WHERE id IN (SELECT template_id FROM personal_templates WHERE owner_id=?) ORDER BY id`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.RuleTemplate{}
	for rows.Next() {
		t, err := s.scanTpl(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) GetPersonalTemplate(ctx context.Context, ownerID, templateID int64) (domain.RuleTemplate, error) {
	t, err := s.scanTpl(s.db.QueryRowContext(ctx, `SELECT `+tplCols+` FROM rule_templates WHERE id=?
 AND EXISTS (SELECT 1 FROM personal_templates WHERE template_id=rule_templates.id AND owner_id=?)`, templateID, ownerID))
	if isNoRows(err) {
		return t, ErrNotFound
	}
	return t, err
}

// UpdatePersonalTemplate uses an ownership predicate in the mutation itself.
func (s *Store) UpdatePersonalTemplate(ctx context.Context, ownerID int64, t *domain.RuleTemplate) error {
	if len(t.Variables) == 0 {
		t.Variables = []byte("{}")
	}
	now := s.Now()
	res, err := s.db.ExecContext(ctx, `UPDATE rule_templates SET name=?,kind=?,description=?,content=?,variables=?,updated_at=?
 WHERE id=? AND EXISTS (SELECT 1 FROM personal_templates WHERE template_id=rule_templates.id AND owner_id=?)
 AND NOT EXISTS (SELECT 1 FROM share_personal_templates WHERE template_id=rule_templates.id)`,
		t.Name, t.Kind, t.Description, s.seal("rule_templates.content", t.Content), s.seal("rule_templates.variables", string(t.Variables)), fmtTime(now), t.ID, ownerID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		var inUse int
		err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM personal_templates p JOIN share_personal_templates st ON st.template_id=p.template_id WHERE p.template_id=? AND p.owner_id=?`, t.ID, ownerID).Scan(&inUse)
		if err != nil {
			return err
		}
		if inUse > 0 {
			return ErrPersonalTemplateInUse
		}
		return ErrNotFound
	}
	t.UpdatedAt = now
	return nil
}

func (s *Store) DeletePersonalTemplate(ctx context.Context, ownerID, templateID int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM personal_templates WHERE template_id=? AND owner_id=?`, templateID, ownerID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListSharePersonalTemplates(ctx context.Context, shareID, ownerID int64) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.format,p.template_id FROM share_personal_templates p
 JOIN shares s ON s.id=p.share_id AND s.user_id=p.owner_id WHERE p.share_id=? AND p.owner_id=?`, shareID, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var format string
		var id int64
		if err := rows.Scan(&format, &id); err != nil {
			return nil, err
		}
		out[format] = id
	}
	return out, rows.Err()
}

// PersonalTemplateOwner returns ErrNotFound for a system template.
func (s *Store) PersonalTemplateOwner(ctx context.Context, templateID int64) (int64, error) {
	var ownerID int64
	err := s.db.QueryRowContext(ctx, `SELECT owner_id FROM personal_templates WHERE template_id=?`, templateID).Scan(&ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return ownerID, err
}

// SetSharePersonalTemplate checks the current owner and format in the same
// statement as the write, preventing reassignment races from restoring access.
func (s *Store) SetSharePersonalTemplate(ctx context.Context, shareID, ownerID, templateID int64, format string, validatedAt time.Time) error {
	res, err := s.db.ExecContext(ctx, `INSERT INTO share_personal_templates(share_id,format,owner_id,template_id)
 SELECT s.id,?,s.user_id,p.template_id FROM shares s JOIN personal_templates p ON p.owner_id=s.user_id
 JOIN rule_templates t ON t.id=p.template_id
 WHERE s.id=? AND s.user_id=? AND p.template_id=? AND t.kind=? AND t.updated_at=?
 ON CONFLICT(share_id,format) DO UPDATE SET owner_id=excluded.owner_id,template_id=excluded.template_id`,
		format, shareID, ownerID, templateID, format, fmtTime(validatedAt))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrNotFound
	}
	return nil
}

// GetSharePersonalTemplate only returns a selection still owned by the share's
// current user; invalid or deleted selections behave as unset.
func (s *Store) GetSharePersonalTemplate(ctx context.Context, shareID int64, format string) (domain.RuleTemplate, error) {
	t, err := s.scanTpl(s.db.QueryRowContext(ctx, `SELECT `+tplCols+` FROM rule_templates WHERE id=(
 SELECT p.template_id FROM share_personal_templates p JOIN shares s ON s.id=p.share_id AND s.user_id=p.owner_id
 JOIN personal_templates o ON o.template_id=p.template_id AND o.owner_id=p.owner_id
 WHERE p.share_id=? AND p.format=?) AND kind=?`, shareID, format, format))
	if isNoRows(err) {
		return t, ErrNotFound
	}
	return t, err
}

// ClearSharePersonalTemplate is scoped to the share's current owner.
func (s *Store) ClearSharePersonalTemplate(ctx context.Context, shareID, ownerID int64, format string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM share_personal_templates WHERE share_id=? AND owner_id=? AND format=?
 AND EXISTS (SELECT 1 FROM shares WHERE id=? AND user_id=?)`, shareID, ownerID, format, shareID, ownerID)
	return err
}
