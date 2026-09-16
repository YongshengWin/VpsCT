package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"ctlvps/internal/auth"
	"ctlvps/internal/domain"
)

// The old uploaded_content column is deliberately left untouched in SQLite so
// retiring configuration hosting does not delete the operator's original data.
const subCols = `id, name, kind, token, token_hash, token_hint, short_code, template_id, default_format, proxy_groups, chains, rules, rule_providers, node_selection, source_external_id, expire_at, traffic_limit_bytes, reset_day, userinfo_header, show_info_nodes, owner_user_id, allowed_user_ids, share_id, enabled, access_count, last_access_at, created_at, updated_at`

func (s *Store) scanSub(sc interface{ Scan(...any) error }) (domain.Subscription, error) {
	var v domain.Subscription
	var templateID, srcExt, shareID sql.NullInt64
	var expire, lastAccess sql.NullString
	var groups, chains, rules, providers, sel, allowed, created, updated string
	var userinfo, showInfo, enabled int
	if err := sc.Scan(&v.ID, &v.Name, &v.Kind, s.scanSecret("subscriptions.token", &v.Token), &v.TokenHash, &v.TokenHint, s.scanSecret("subscriptions.short_code", &v.ShortCode), &templateID, &v.DefaultFormat, &groups, &chains, &rules, &providers, &sel,
		&srcExt, &expire, &v.TrafficLimitBytes, &v.ResetDay, &userinfo, &showInfo, &v.OwnerUserID, &allowed, &shareID, &enabled, &v.AccessCount, &lastAccess, &created, &updated); err != nil {
		return v, err
	}
	v.TemplateID = intPtr(templateID)
	v.SourceExternalID = intPtr(srcExt)
	v.ShareID = intPtr(shareID)
	v.ExpireAt = parseTimePtr(expire)
	v.LastAccessAt = parseTimePtr(lastAccess)
	v.ProxyGroups = jsonList[domain.ProxyGroup](groups)
	v.Chains = jsonList[domain.ChainSpec](chains)
	v.Rules = jsonList[string](rules)
	v.RuleProviders = rawOrEmpty(providers)
	_ = json.Unmarshal([]byte(sel), &v.NodeSelection)
	if v.NodeSelection.NodeIDs == nil {
		v.NodeSelection.NodeIDs = []int64{}
	}
	if v.NodeSelection.ExternalSubIDs == nil {
		v.NodeSelection.ExternalSubIDs = []int64{}
	}
	v.AllowedUserIDs = jsonList[int64](allowed)
	v.UserinfoHeader = userinfo == 1
	v.ShowInfoNodes = showInfo == 1
	v.Enabled = enabled == 1
	v.CreatedAt = parseTime(created)
	v.UpdatedAt = parseTime(updated)
	return v, nil
}

func (s *Store) subArgs(v *domain.Subscription) []any {
	if v.ProxyGroups == nil {
		v.ProxyGroups = []domain.ProxyGroup{}
	}
	if v.Chains == nil {
		v.Chains = []domain.ChainSpec{}
	}
	if v.Rules == nil {
		v.Rules = []string{}
	}
	if len(v.RuleProviders) == 0 {
		v.RuleProviders = []byte("{}")
	}
	if v.AllowedUserIDs == nil {
		v.AllowedUserIDs = []int64{}
	}
	if v.DefaultFormat == "" {
		v.DefaultFormat = "mihomo"
	}
	return []any{v.Name, v.Kind, s.seal("subscriptions.token", v.Token), v.TokenHash, v.TokenHint, s.seal("subscriptions.short_code", v.ShortCode), auth.HashToken(v.ShortCode), nullInt(v.TemplateID), v.DefaultFormat, jsonStr(v.ProxyGroups), jsonStr(v.Chains), jsonStr(v.Rules), string(v.RuleProviders), jsonStr(v.NodeSelection),
		nullInt(v.SourceExternalID), fmtTimePtr(v.ExpireAt), v.TrafficLimitBytes, v.ResetDay, b2i(v.UserinfoHeader), b2i(v.ShowInfoNodes), v.OwnerUserID, jsonStr(v.AllowedUserIDs), nullInt(v.ShareID), b2i(v.Enabled)}
}

// CreateSubscription inserts a subscription.
func (s *Store) CreateSubscription(ctx context.Context, v *domain.Subscription) error {
	now := s.Now()
	args := append(s.subArgs(v), fmtTime(now), fmtTime(now))
	res, err := s.db.ExecContext(ctx, `INSERT INTO subscriptions(name,kind,token,token_hash,token_hint,short_code,short_code_hash,template_id,default_format,proxy_groups,chains,rules,rule_providers,node_selection,source_external_id,expire_at,traffic_limit_bytes,reset_day,userinfo_header,show_info_nodes,owner_user_id,allowed_user_ids,share_id,enabled,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, args...)
	if err != nil {
		return err
	}
	v.ID, _ = res.LastInsertId()
	v.CreatedAt, v.UpdatedAt = now, now
	return nil
}

// UpdateSubscription saves all editable fields.
func (s *Store) UpdateSubscription(ctx context.Context, v *domain.Subscription) error {
	now := s.Now()
	args := append(s.subArgs(v), fmtTime(now), v.ID)
	_, err := s.db.ExecContext(ctx, `UPDATE subscriptions SET name=?,kind=?,token=?,token_hash=?,token_hint=?,short_code=?,short_code_hash=?,template_id=?,default_format=?,proxy_groups=?,chains=?,rules=?,rule_providers=?,node_selection=?,source_external_id=?,expire_at=?,traffic_limit_bytes=?,reset_day=?,userinfo_header=?,show_info_nodes=?,owner_user_id=?,allowed_user_ids=?,share_id=?,enabled=?,updated_at=? WHERE id=?`, args...)
	v.UpdatedAt = now
	return err
}

// DeleteSubscription removes a subscription.
func (s *Store) DeleteSubscription(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM subscriptions WHERE id=?`, id)
	return err
}

// GetSubscription fetches one.
func (s *Store) GetSubscription(ctx context.Context, id int64) (domain.Subscription, error) {
	v, err := s.scanSub(s.db.QueryRowContext(ctx, `SELECT `+subCols+` FROM subscriptions WHERE id=?`, id))
	if isNoRows(err) {
		return v, ErrNotFound
	}
	return v, err
}

// GetSubscriptionByTokenHash resolves /s/<token>.
func (s *Store) GetSubscriptionByTokenHash(ctx context.Context, hash string) (domain.Subscription, error) {
	v, err := s.scanSub(s.db.QueryRowContext(ctx, `SELECT `+subCols+` FROM subscriptions WHERE token_hash=?`, hash))
	if isNoRows(err) {
		return v, ErrNotFound
	}
	return v, err
}

// GetSubscriptionByShortCode resolves /r/<code>.
func (s *Store) GetSubscriptionByShortCode(ctx context.Context, code string) (domain.Subscription, error) {
	if code == "" {
		return domain.Subscription{}, ErrNotFound
	}
	v, err := s.scanSub(s.db.QueryRowContext(ctx, `SELECT `+subCols+` FROM subscriptions WHERE short_code_hash=?`, auth.HashToken(code)))
	if isNoRows(err) {
		return v, ErrNotFound
	}
	return v, err
}

// GetSubscriptionByShare returns the share's subscription.
func (s *Store) GetSubscriptionByShare(ctx context.Context, shareID int64) (domain.Subscription, error) {
	v, err := s.scanSub(s.db.QueryRowContext(ctx, `SELECT `+subCols+` FROM subscriptions WHERE share_id=? ORDER BY id LIMIT 1`, shareID))
	if isNoRows(err) {
		return v, ErrNotFound
	}
	return v, err
}

// ListSubscriptions returns all subscriptions.
func (s *Store) ListSubscriptions(ctx context.Context) ([]domain.Subscription, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+subCols+` FROM subscriptions ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Subscription{}
	for rows.Next() {
		v, err := s.scanSub(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// TouchSubscription bumps access statistics.
func (s *Store) TouchSubscription(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE subscriptions SET access_count=access_count+1, last_access_at=? WHERE id=?`, fmtTime(s.Now()), id)
	return err
}

// ---- templates ----

const tplCols = `id, name, kind, description, content, variables, is_builtin, created_at, updated_at`

func (s *Store) scanTpl(sc interface{ Scan(...any) error }) (domain.RuleTemplate, error) {
	var t domain.RuleTemplate
	var vars, created, updated string
	var builtin int
	if err := sc.Scan(&t.ID, &t.Name, &t.Kind, &t.Description, s.scanSecret("rule_templates.content", &t.Content), s.scanSecret("rule_templates.variables", &vars), &builtin, &created, &updated); err != nil {
		return t, err
	}
	t.Variables = rawOrEmpty(vars)
	t.IsBuiltin = builtin == 1
	t.CreatedAt = parseTime(created)
	t.UpdatedAt = parseTime(updated)
	return t, nil
}

// CreateTemplate inserts a template.
func (s *Store) CreateTemplate(ctx context.Context, t *domain.RuleTemplate) error {
	now := s.Now()
	if t.Kind == "" {
		t.Kind = "mihomo"
	}
	if len(t.Variables) == 0 {
		t.Variables = []byte("{}")
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO rule_templates(name,kind,description,content,variables,is_builtin,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?)`,
		t.Name, t.Kind, t.Description, s.seal("rule_templates.content", t.Content), s.seal("rule_templates.variables", string(t.Variables)), b2i(t.IsBuiltin), fmtTime(now), fmtTime(now))
	if err != nil {
		return err
	}
	t.ID, _ = res.LastInsertId()
	t.CreatedAt, t.UpdatedAt = now, now
	return nil
}

// UpdateTemplate saves a template.
func (s *Store) UpdateTemplate(ctx context.Context, t *domain.RuleTemplate) error {
	now := s.Now()
	if len(t.Variables) == 0 {
		t.Variables = []byte("{}")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE rule_templates SET name=?, kind=?, description=?, content=?, variables=?, updated_at=? WHERE id=?`,
		t.Name, t.Kind, t.Description, s.seal("rule_templates.content", t.Content), s.seal("rule_templates.variables", string(t.Variables)), fmtTime(now), t.ID)
	t.UpdatedAt = now
	return err
}

// DeleteTemplate removes a template.
func (s *Store) DeleteTemplate(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM rule_templates WHERE id=?`, id)
	return err
}

// GetTemplate fetches one.
func (s *Store) GetTemplate(ctx context.Context, id int64) (domain.RuleTemplate, error) {
	t, err := s.scanTpl(s.db.QueryRowContext(ctx, `SELECT `+tplCols+` FROM rule_templates WHERE id=?`, id))
	if isNoRows(err) {
		return t, ErrNotFound
	}
	return t, err
}

// ListTemplates returns all templates.
func (s *Store) ListTemplates(ctx context.Context) ([]domain.RuleTemplate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+tplCols+` FROM rule_templates ORDER BY is_builtin DESC, id`)
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

// CountTemplatesByName is used to seed builtins idempotently.
func (s *Store) CountTemplatesByName(ctx context.Context, name string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rule_templates WHERE name=?`, name).Scan(&n)
	return n, err
}

// ---- proxy group presets ----

const presetCols = `id, name, groups_json, rules_json, is_builtin, created_at, updated_at`

func (s *Store) scanPreset(sc interface{ Scan(...any) error }) (domain.ProxyGroupPreset, error) {
	var p domain.ProxyGroupPreset
	var groups, rules, created, updated string
	var builtin int
	if err := sc.Scan(&p.ID, &p.Name, s.scanSecret("proxy_group_presets.groups_json", &groups), s.scanSecret("proxy_group_presets.rules_json", &rules), &builtin, &created, &updated); err != nil {
		return p, err
	}
	p.Groups = jsonList[domain.ProxyGroup](groups)
	p.Rules = jsonList[string](rules)
	p.IsBuiltin = builtin == 1
	p.CreatedAt = parseTime(created)
	p.UpdatedAt = parseTime(updated)
	return p, nil
}

// CreatePreset inserts a preset.
func (s *Store) CreatePreset(ctx context.Context, p *domain.ProxyGroupPreset) error {
	now := s.Now()
	if p.Groups == nil {
		p.Groups = []domain.ProxyGroup{}
	}
	if p.Rules == nil {
		p.Rules = []string{}
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO proxy_group_presets(name,groups_json,rules_json,is_builtin,created_at,updated_at) VALUES (?,?,?,?,?,?)`,
		p.Name, s.seal("proxy_group_presets.groups_json", jsonStr(p.Groups)), s.seal("proxy_group_presets.rules_json", jsonStr(p.Rules)), b2i(p.IsBuiltin), fmtTime(now), fmtTime(now))
	if err != nil {
		return err
	}
	p.ID, _ = res.LastInsertId()
	p.CreatedAt, p.UpdatedAt = now, now
	return nil
}

// UpdatePreset saves a preset.
func (s *Store) UpdatePreset(ctx context.Context, p *domain.ProxyGroupPreset) error {
	now := s.Now()
	_, err := s.db.ExecContext(ctx, `UPDATE proxy_group_presets SET name=?, groups_json=?, rules_json=?, updated_at=? WHERE id=?`,
		p.Name, s.seal("proxy_group_presets.groups_json", jsonStr(p.Groups)), s.seal("proxy_group_presets.rules_json", jsonStr(p.Rules)), fmtTime(now), p.ID)
	p.UpdatedAt = now
	return err
}

// DeletePreset removes a preset.
func (s *Store) DeletePreset(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM proxy_group_presets WHERE id=?`, id)
	return err
}

// ListPresets returns all presets.
func (s *Store) ListPresets(ctx context.Context) ([]domain.ProxyGroupPreset, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+presetCols+` FROM proxy_group_presets ORDER BY is_builtin DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.ProxyGroupPreset{}
	for rows.Next() {
		p, err := s.scanPreset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CountPresetsByName is used to seed builtins idempotently.
func (s *Store) CountPresetsByName(ctx context.Context, name string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM proxy_group_presets WHERE name=?`, name).Scan(&n)
	return n, err
}
