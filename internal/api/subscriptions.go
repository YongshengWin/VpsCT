package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ctlvps/internal/auth"
	"ctlvps/internal/domain"
	"ctlvps/internal/httpx"
	"ctlvps/internal/subscription"
	"ctlvps/internal/traffic"
)

// SubscriptionView adds ready-to-copy links.
type SubscriptionView struct {
	domain.Subscription
	Links     map[string]string `json:"links"`
	ShortLink string            `json:"short_link,omitempty"`
	NodeCount int               `json:"node_count"`
	Supported bool              `json:"supported"`
	ShareName string            `json:"share_name,omitempty"`
	NextReset *time.Time        `json:"next_reset,omitempty"`
}

func (a *API) subView(r *http.Request, s domain.Subscription, full bool) SubscriptionView {
	v := SubscriptionView{Subscription: s, Links: map[string]string{}, Supported: s.Kind.Supported()}
	if !v.Supported {
		v.Enabled = false
		v.Token, v.TokenHint, v.ShortCode = "", "", ""
		return v
	}
	base := a.baseURL(r)
	if s.Token != "" {
		for _, f := range subscription.KnownFormats {
			v.Links[f] = base + "/s/" + s.Token + "/" + f
		}
		v.Links["auto"] = base + "/s/" + s.Token
	}
	if s.ShortCode != "" {
		v.ShortLink = base + "/r/" + s.ShortCode
	}
	if !full {
		v.Token = ""
	}
	if s.ShareID != nil {
		if sh, err := a.Store.GetShare(r.Context(), *s.ShareID); err == nil {
			v.ShareName = sh.Name
		}
	}
	if b, err := a.Subs.Build(r.Context(), s); err == nil {
		v.NodeCount = len(b.Proxies) + len(b.Chains)
	}
	if s.ResetDay > 0 {
		nr := traffic.NextReset(a.Store.Now(), s.ResetDay)
		v.NextReset = &nr
	}
	return v
}

func (a *API) canSeeSubscription(u *domain.User, s domain.Subscription) bool {
	if isAdmin(u) {
		return true
	}
	if s.OwnerUserID == u.ID {
		return true
	}
	for _, id := range s.AllowedUserIDs {
		if id == u.ID {
			return true
		}
	}
	return false
}

func (a *API) listSubscriptions(w http.ResponseWriter, r *http.Request) error {
	u := userFrom(r.Context())
	list, err := a.Store.ListSubscriptions(r.Context())
	if err != nil {
		return err
	}
	out := []SubscriptionView{}
	for _, s := range list {
		if !a.canSeeSubscription(u, s) {
			continue
		}
		if k := r.URL.Query().Get("kind"); k != "" && string(s.Kind) != k {
			continue
		}
		out = append(out, a.subView(r, s, true))
	}
	httpx.OK(w, out)
	return nil
}

type subscriptionInput struct {
	Name          string                  `json:"name"`
	Kind          domain.SubscriptionKind `json:"kind"`
	TemplateID    *int64                  `json:"template_id"`
	DefaultFormat string                  `json:"default_format"`
	ProxyGroups   []domain.ProxyGroup     `json:"proxy_groups"`
	Chains        []domain.ChainSpec      `json:"chains"`
	Rules         []string                `json:"rules"`
	RuleProviders json.RawMessage         `json:"rule_providers"`
	NodeSelection *domain.NodeSelection   `json:"node_selection"`
	// Reject old clients explicitly instead of silently ignoring their config.
	RetiredContent    json.RawMessage `json:"uploaded_content"`
	SourceExternalID  *int64          `json:"source_external_id"`
	ExpireAt          *time.Time      `json:"expire_at"`
	TrafficLimitBytes *int64          `json:"traffic_limit_bytes"`
	ResetDay          *int            `json:"reset_day"`
	UserinfoHeader    *bool           `json:"userinfo_header"`
	ShowInfoNodes     *bool           `json:"show_info_nodes"`
	AllowedUserIDs    []int64         `json:"allowed_user_ids"`
	Enabled           *bool           `json:"enabled"`
	ShortLink         *bool           `json:"short_link"`
}

func (a *API) applySubInput(in subscriptionInput, s *domain.Subscription, creating bool) error {
	if len(in.RetiredContent) > 0 {
		return httpx.BadRequest("已移除配置上传，请从节点库生成订阅并选择模板")
	}
	if !creating && !s.Kind.Supported() {
		return httpx.E(http.StatusGone, "unsupported_kind", "此订阅类型已停用，请重新生成订阅")
	}
	if !creating && in.Kind != "" && in.Kind != s.Kind {
		return httpx.BadRequest("不能更改订阅类型")
	}
	if strings.TrimSpace(in.Name) != "" {
		name := strings.TrimSpace(in.Name)
		if len([]rune(name)) > 64 {
			return httpx.BadRequest("名称最多 64 字")
		}
		s.Name = name
	}
	if s.Name == "" {
		return httpx.BadRequest("名称不能为空")
	}
	if creating {
		switch in.Kind {
		case domain.SubGenerated, domain.SubImported:
			s.Kind = in.Kind
		case "":
			s.Kind = domain.SubGenerated
		default:
			return httpx.BadRequest("kind 无效")
		}
	}
	if in.TemplateID != nil {
		if *in.TemplateID == 0 {
			s.TemplateID = nil
		} else {
			s.TemplateID = in.TemplateID
		}
	}
	if in.DefaultFormat != "" {
		if subscription.NormalizeFormat(in.DefaultFormat) == "" {
			return httpx.BadRequest("default_format 无效")
		}
		s.DefaultFormat = subscription.NormalizeFormat(in.DefaultFormat)
	}
	if in.ProxyGroups != nil {
		if errs := subscription.ValidateGroups(in.ProxyGroups); len(errs) > 0 {
			return httpx.BadRequest(strings.Join(errs, "；"))
		}
		s.ProxyGroups = in.ProxyGroups
	}
	if in.Chains != nil {
		s.Chains = in.Chains
	}
	if in.Rules != nil {
		s.Rules = in.Rules
	}
	if len(in.RuleProviders) > 0 {
		var m map[string]any
		if err := json.Unmarshal(in.RuleProviders, &m); err != nil {
			return httpx.BadRequest("rule_providers 必须是 JSON 对象")
		}
		s.RuleProviders = in.RuleProviders
	}
	if in.NodeSelection != nil {
		s.NodeSelection = *in.NodeSelection
	}
	if s.Kind == domain.SubImported {
		if in.SourceExternalID != nil {
			s.SourceExternalID = in.SourceExternalID
		}
		if s.SourceExternalID == nil {
			return httpx.BadRequest("导入订阅必须选择来源")
		}
	}
	if in.ExpireAt != nil {
		if in.ExpireAt.IsZero() {
			s.ExpireAt = nil
		} else {
			s.ExpireAt = in.ExpireAt
		}
	}
	if in.TrafficLimitBytes != nil {
		s.TrafficLimitBytes = *in.TrafficLimitBytes
	}
	if in.ResetDay != nil {
		if *in.ResetDay < 0 || *in.ResetDay > 31 {
			return httpx.BadRequest("重置日必须是 0–31（29–31 为每月最后一天）")
		}
		s.ResetDay = domain.NormalizeResetDay(*in.ResetDay)
	}
	if in.UserinfoHeader != nil {
		s.UserinfoHeader = *in.UserinfoHeader
	}
	if in.ShowInfoNodes != nil {
		s.ShowInfoNodes = *in.ShowInfoNodes
	}
	if in.AllowedUserIDs != nil {
		s.AllowedUserIDs = in.AllowedUserIDs
	}
	if in.Enabled != nil {
		s.Enabled = *in.Enabled
	}
	if in.ShortLink != nil {
		if *in.ShortLink && s.ShortCode == "" {
			s.ShortCode = auth.NewSubscriptionToken()
		} else if !*in.ShortLink {
			s.ShortCode = ""
		}
	}
	return nil
}

func (a *API) createSubscription(w http.ResponseWriter, r *http.Request) error {
	var in subscriptionInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	token := auth.NewSubscriptionToken()
	s := domain.Subscription{
		Token: token, TokenHash: auth.HashToken(token), TokenHint: auth.TokenHint(token),
		DefaultFormat: "mihomo", UserinfoHeader: a.Store.GetSettingBool(r.Context(), domain.SettingUserinfoDefault, true), Enabled: true,
		OwnerUserID: userFrom(r.Context()).ID,
	}
	if a.Store.GetSettingBool(r.Context(), domain.SettingShortLinks, true) {
		s.ShortCode = auth.NewSubscriptionToken()
	}
	if err := a.applySubInput(in, &s, true); err != nil {
		return err
	}
	if err := a.Store.CreateSubscription(r.Context(), &s); err != nil {
		return err
	}
	a.audit(r, "subscription.create", s.Name, map[string]any{"kind": s.Kind})
	httpx.JSON(w, http.StatusCreated, a.subView(r, s, true))
	return nil
}

func (a *API) getSubscription(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	s, err := a.Store.GetSubscription(r.Context(), id)
	if err != nil {
		return httpx.ErrNotFound
	}
	if !a.canSeeSubscription(userFrom(r.Context()), s) {
		return httpx.ErrForbidden
	}
	httpx.OK(w, a.subView(r, s, true))
	return nil
}

func (a *API) updateSubscription(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	s, err := a.Store.GetSubscription(r.Context(), id)
	if err != nil {
		return httpx.ErrNotFound
	}
	var in subscriptionInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := a.applySubInput(in, &s, false); err != nil {
		return err
	}
	if err := a.Store.UpdateSubscription(r.Context(), &s); err != nil {
		return err
	}
	if s.Kind == domain.SubShare && s.ShareID != nil {
		if sh, err := a.Store.GetShare(r.Context(), *s.ShareID); err == nil && sh.Name != s.Name {
			sh.Name = s.Name
			if err := a.Store.UpdateShare(r.Context(), &sh); err != nil {
				return err
			}
		}
	}
	a.audit(r, "subscription.update", s.Name, nil)
	httpx.OK(w, a.subView(r, s, true))
	return nil
}

func (a *API) deleteSubscription(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	s, err := a.Store.GetSubscription(r.Context(), id)
	if err != nil {
		return httpx.ErrNotFound
	}
	if s.Kind == domain.SubShare {
		return httpx.BadRequest("分享订阅随分享一起删除")
	}
	if err := a.Store.DeleteSubscription(r.Context(), id); err != nil {
		return err
	}
	a.audit(r, "subscription.delete", s.Name, nil)
	httpx.NoContent(w)
	return nil
}

func (a *API) rotateSubscriptionToken(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	s, err := a.Store.GetSubscription(r.Context(), id)
	if err != nil {
		return httpx.ErrNotFound
	}
	if !s.Kind.Supported() {
		return httpx.E(http.StatusGone, "unsupported_kind", "此订阅类型已停用，请重新生成订阅")
	}
	token := auth.NewSubscriptionToken()
	s.Token, s.TokenHash, s.TokenHint = token, auth.HashToken(token), auth.TokenHint(token)
	if s.ShortCode != "" {
		s.ShortCode = auth.NewSubscriptionToken()
	}
	if err := a.Store.UpdateSubscription(r.Context(), &s); err != nil {
		return err
	}
	a.audit(r, "subscription.rotate_token", s.Name, nil)
	httpx.OK(w, a.subView(r, s, true))
	return nil
}

func (a *API) renderSubscription(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	s, err := a.Store.GetSubscription(r.Context(), id)
	if err != nil {
		return httpx.ErrNotFound
	}
	if !a.canSeeSubscription(userFrom(r.Context()), s) {
		return httpx.ErrForbidden
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = s.DefaultFormat
	}
	rendered, bundle, err := a.Subs.Render(r.Context(), s, format)
	if err != nil {
		return httpx.BadRequest(err.Error())
	}
	httpx.OK(w, map[string]any{"format": rendered.Format, "content_type": rendered.ContentType, "body": string(rendered.Body), "node_count": len(bundle.Proxies) + len(bundle.Chains)})
	return nil
}

// previewSubscription renders an unsaved subscription definition.
func (a *API) previewSubscription(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		subscriptionInput
		Format string `json:"format"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	s := domain.Subscription{Name: "preview", DefaultFormat: "mihomo", Enabled: true}
	if err := a.applySubInput(in.subscriptionInput, &s, true); err != nil {
		return err
	}
	if in.Format == "" {
		in.Format = s.DefaultFormat
	}
	rendered, bundle, err := a.Subs.Render(r.Context(), s, in.Format)
	if err != nil {
		return httpx.BadRequest(err.Error())
	}
	names := make([]string, 0, len(bundle.Proxies))
	for _, p := range bundle.Proxies {
		names = append(names, p.Name)
	}
	for _, c := range bundle.Chains {
		names = append(names, c.Proxy.Name)
	}
	httpx.OK(w, map[string]any{"format": rendered.Format, "body": string(rendered.Body), "node_names": names, "groups": bundle.Groups})
	return nil
}

func (a *API) validateGroups(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Groups []domain.ProxyGroup `json:"proxy_groups"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	httpx.OK(w, map[string]any{"errors": subscription.ValidateGroups(in.Groups)})
	return nil
}

func (a *API) subscriptionAccessLog(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	list, err := a.Store.ListAccessLog(r.Context(), &id, httpx.QueryInt(r, "limit", 100), httpx.QueryInt(r, "offset", 0))
	if err != nil {
		return err
	}
	httpx.OK(w, list)
	return nil
}

// ---- templates ----

func (a *API) listTemplates(w http.ResponseWriter, r *http.Request) error {
	list, err := a.Store.ListTemplates(r.Context())
	if err != nil {
		return err
	}
	httpx.OK(w, list)
	return nil
}

type templateInput struct {
	Name        string          `json:"name"`
	Kind        string          `json:"kind"`
	Description string          `json:"description"`
	Content     string          `json:"content"`
	Variables   json.RawMessage `json:"variables"`
}

func (in templateInput) apply(t *domain.RuleTemplate) error {
	if strings.TrimSpace(in.Name) == "" {
		return httpx.BadRequest("名称不能为空")
	}
	switch in.Kind {
	case "", "mihomo":
		t.Kind = "mihomo"
	case "surge", "singbox", "shadowrocket":
		t.Kind = in.Kind
	default:
		return httpx.BadRequest("kind 必须是 mihomo/surge/singbox/shadowrocket")
	}
	t.Name = strings.TrimSpace(in.Name)
	t.Description = in.Description
	t.Content = in.Content
	if len(in.Variables) > 0 {
		t.Variables = in.Variables
	}
	// validate syntax by rendering an empty bundle
	b := &subscription.Bundle{Name: "check", Template: t}
	if _, err := subscription.RenderBundle(b, t.Kind); err != nil {
		return httpx.BadRequest("模板无法渲染: " + err.Error())
	}
	return nil
}

func (a *API) createTemplate(w http.ResponseWriter, r *http.Request) error {
	var in templateInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	t := domain.RuleTemplate{}
	if err := in.apply(&t); err != nil {
		return err
	}
	if err := a.Store.CreateTemplate(r.Context(), &t); err != nil {
		return err
	}
	a.audit(r, "template.create", t.Name, nil)
	httpx.JSON(w, http.StatusCreated, t)
	return nil
}

func (a *API) updateTemplate(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	t, err := a.Store.GetTemplate(r.Context(), id)
	if err != nil {
		return httpx.ErrNotFound
	}
	var in templateInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := in.apply(&t); err != nil {
		return err
	}
	t.IsBuiltin = false
	if err := a.Store.UpdateTemplate(r.Context(), &t); err != nil {
		return err
	}
	a.audit(r, "template.update", t.Name, nil)
	httpx.OK(w, t)
	return nil
}

func (a *API) deleteTemplate(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	if err := a.Store.DeleteTemplate(r.Context(), id); err != nil {
		return err
	}
	a.audit(r, "template.delete", strconv.FormatInt(id, 10), nil)
	httpx.NoContent(w)
	return nil
}

// ---- presets ----

func (a *API) listPresets(w http.ResponseWriter, r *http.Request) error {
	list, err := a.Store.ListPresets(r.Context())
	if err != nil {
		return err
	}
	httpx.OK(w, list)
	return nil
}

type presetInput struct {
	Name   string              `json:"name"`
	Groups []domain.ProxyGroup `json:"groups"`
	Rules  []string            `json:"rules"`
}

func (a *API) createPreset(w http.ResponseWriter, r *http.Request) error {
	var in presetInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if strings.TrimSpace(in.Name) == "" {
		return httpx.BadRequest("名称不能为空")
	}
	if errs := subscription.ValidateGroups(in.Groups); len(errs) > 0 {
		return httpx.BadRequest(strings.Join(errs, "；"))
	}
	p := domain.ProxyGroupPreset{Name: strings.TrimSpace(in.Name), Groups: in.Groups, Rules: in.Rules}
	if err := a.Store.CreatePreset(r.Context(), &p); err != nil {
		return err
	}
	a.audit(r, "preset.create", p.Name, nil)
	httpx.JSON(w, http.StatusCreated, p)
	return nil
}

func (a *API) updatePreset(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	var in presetInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if errs := subscription.ValidateGroups(in.Groups); len(errs) > 0 {
		return httpx.BadRequest(strings.Join(errs, "；"))
	}
	p := domain.ProxyGroupPreset{ID: id, Name: strings.TrimSpace(in.Name), Groups: in.Groups, Rules: in.Rules}
	if err := a.Store.UpdatePreset(r.Context(), &p); err != nil {
		return err
	}
	a.audit(r, "preset.update", p.Name, nil)
	httpx.OK(w, p)
	return nil
}

func (a *API) deletePreset(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	if err := a.Store.DeletePreset(r.Context(), id); err != nil {
		return err
	}
	httpx.NoContent(w)
	return nil
}
