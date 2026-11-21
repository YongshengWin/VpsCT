package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ctlvps/internal/domain"
	"ctlvps/internal/httpx"
	"ctlvps/internal/store"
	"ctlvps/internal/subscription"
	"gopkg.in/yaml.v3"
)

const maxPersonalTemplates = 32
const maxPersonalTemplateBytes = 256 << 10

func validatePersonalTemplate(in templateInput, t *domain.RuleTemplate) error {
	if len(in.Name) > 120 || len(in.Description) > 1000 || len(in.Content) > maxPersonalTemplateBytes || len(in.Variables) > maxPersonalTemplateBytes {
		return httpx.BadRequest("模板名称、描述或内容超过大小限制")
	}
	return in.apply(t)
}

func (a *API) listPersonalTemplates(w http.ResponseWriter, r *http.Request) error {
	list, err := a.Store.ListPersonalTemplates(r.Context(), userFrom(r.Context()).ID)
	if err != nil {
		return err
	}
	httpx.OK(w, list)
	return nil
}

func (a *API) createPersonalTemplate(w http.ResponseWriter, r *http.Request) error {
	var in templateInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	t := domain.RuleTemplate{}
	if err := validatePersonalTemplate(in, &t); err != nil {
		return err
	}
	if err := a.Store.CreatePersonalTemplate(r.Context(), userFrom(r.Context()).ID, &t, maxPersonalTemplates); err != nil {
		if errors.Is(err, store.ErrPersonalTemplateLimit) {
			return httpx.Conflict("账户不可用或个人模板数量已达上限")
		}
		return err
	}
	_ = a.audit(r, "personal_template.create", strconv.FormatInt(t.ID, 10), nil)
	httpx.JSON(w, http.StatusCreated, t)
	return nil
}

func (a *API) personalTemplate(r *http.Request) (domain.RuleTemplate, error) {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return domain.RuleTemplate{}, err
	}
	t, err := a.Store.GetPersonalTemplate(r.Context(), userFrom(r.Context()).ID, id)
	if errors.Is(err, store.ErrNotFound) {
		return t, httpx.ErrNotFound
	}
	return t, err
}

func (a *API) getPersonalTemplate(w http.ResponseWriter, r *http.Request) error {
	t, err := a.personalTemplate(r)
	if err != nil {
		return err
	}
	httpx.OK(w, t)
	return nil
}

func (a *API) updatePersonalTemplate(w http.ResponseWriter, r *http.Request) error {
	t, err := a.personalTemplate(r)
	if err != nil {
		return err
	}
	var in templateInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := validatePersonalTemplate(in, &t); err != nil {
		return err
	}
	if err := a.Store.UpdatePersonalTemplate(r.Context(), userFrom(r.Context()).ID, &t); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return httpx.ErrNotFound
		}
		if errors.Is(err, store.ErrPersonalTemplateInUse) || strings.Contains(err.Error(), "personal template is in use") {
			return httpx.Conflict("模板正在使用，请先复制为新模板，编辑并预览后应用")
		}
		return err
	}
	_ = a.audit(r, "personal_template.update", strconv.FormatInt(t.ID, 10), nil)
	httpx.OK(w, t)
	return nil
}

func (a *API) deletePersonalTemplate(w http.ResponseWriter, r *http.Request) error {
	t, err := a.personalTemplate(r)
	if err != nil {
		return err
	}
	if err := a.Store.DeletePersonalTemplate(r.Context(), userFrom(r.Context()).ID, t.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return httpx.ErrNotFound
		}
		return err
	}
	_ = a.audit(r, "personal_template.delete", strconv.FormatInt(t.ID, 10), nil)
	httpx.NoContent(w)
	return nil
}

func (a *API) ownShare(r *http.Request) (domain.Share, error) {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return domain.Share{}, err
	}
	sh, err := a.Store.GetShare(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		return sh, httpx.ErrNotFound
	}
	if err != nil {
		return sh, err
	}
	if sh.UserID == nil || *sh.UserID != userFrom(r.Context()).ID {
		return sh, httpx.ErrForbidden
	}
	return sh, nil
}

func (a *API) listSharePersonalTemplates(w http.ResponseWriter, r *http.Request) error {
	sh, err := a.ownShare(r)
	if err != nil {
		return err
	}
	selected, err := a.Store.ListSharePersonalTemplates(r.Context(), sh.ID, *sh.UserID)
	if err != nil {
		return err
	}
	httpx.OK(w, selected)
	return nil
}

type personalTemplateSelection struct {
	TemplateID int64  `json:"template_id"`
	Format     string `json:"format"`
}

type personalTemplateDraftInput struct {
	Content string `json:"content"`
}

type personalTemplatePreviewResult struct {
	Format string `json:"format"`
	Body   string `json:"body"`
}

type templatePreviewGroup struct {
	Name    string   `json:"name"`
	Members []string `json:"members"`
}
type templatePreviewNode struct {
	Name   string `json:"name"`
	Region string `json:"region"`
	Source string `json:"source"`
}
type personalTemplateDraftResult struct {
	Format   string                 `json:"format"`
	Body     string                 `json:"body"`
	Groups   []templatePreviewGroup `json:"groups"`
	Nodes    []templatePreviewNode  `json:"nodes"`
	Warnings []string               `json:"warnings"`
}

func (a *API) previewPersonalTemplate(r *http.Request, sh domain.Share, templateID int64, format string) (*subscription.Rendered, time.Time, error) {
	t, err := a.Store.GetPersonalTemplate(r.Context(), *sh.UserID, templateID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, time.Time{}, httpx.ErrNotFound
	}
	if err != nil {
		return nil, time.Time{}, err
	}
	format = subscription.NormalizeFormat(format)
	if format == "" || t.Kind != format {
		return nil, time.Time{}, httpx.BadRequest("模板格式与客户端格式不匹配")
	}
	sub, err := a.Store.GetSubscriptionByShare(r.Context(), sh.ID)
	if err != nil {
		return nil, time.Time{}, err
	}
	b, err := a.Subs.Build(r.Context(), sub)
	if err != nil {
		return nil, time.Time{}, err
	}
	b.Template = &t
	rendered, err := subscription.RenderBundle(b, format)
	if err != nil {
		return nil, time.Time{}, httpx.BadRequest("模板无法渲染: " + err.Error())
	}
	return rendered, t.UpdatedAt, nil
}

func (a *API) previewSharePersonalTemplate(w http.ResponseWriter, r *http.Request) error {
	sh, err := a.ownShare(r)
	if err != nil {
		return err
	}
	var in personalTemplateSelection
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	rendered, _, err := a.previewPersonalTemplate(r, sh, in.TemplateID, in.Format)
	if err != nil {
		return err
	}
	httpx.OK(w, personalTemplatePreviewResult{Format: rendered.Format, Body: string(rendered.Body)})
	return nil
}

// Preview an unsaved draft using only the caller's own share. This never saves
// or selects the template and must not expose nodes belonging to another user.
func (a *API) previewShareTemplateDraft(w http.ResponseWriter, r *http.Request) error {
	sh, err := a.ownShare(r)
	if err != nil {
		return err
	}
	var in personalTemplateDraftInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if len(in.Content) > maxPersonalTemplateBytes || strings.TrimSpace(in.Content) == "" {
		return httpx.BadRequest("模板内容为空或超过大小限制")
	}
	return a.renderShareTemplateDraft(w, r, sh, in)
}

func (a *API) previewAdminShareTemplateDraft(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	sh, err := a.Store.GetShare(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		return httpx.ErrNotFound
	}
	if err != nil {
		return err
	}
	var in personalTemplateDraftInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if len(in.Content) > maxPersonalTemplateBytes || strings.TrimSpace(in.Content) == "" {
		return httpx.BadRequest("模板内容为空或超过大小限制")
	}
	return a.renderShareTemplateDraft(w, r, sh, in)
}

func (a *API) renderShareTemplateDraft(w http.ResponseWriter, r *http.Request, sh domain.Share, in personalTemplateDraftInput) error {
	sub, err := a.Store.GetSubscriptionByShare(r.Context(), sh.ID)
	if err != nil {
		return err
	}
	b, err := a.Subs.Build(r.Context(), sub)
	if err != nil {
		return err
	}
	b.Template = &domain.RuleTemplate{Kind: "mihomo", Content: in.Content}
	rendered, err := subscription.RenderMihomo(b)
	if err != nil {
		return httpx.BadRequest("模板无法渲染: " + err.Error())
	}
	var config struct {
		Groups []struct {
			Name    string   `yaml:"name"`
			Members []string `yaml:"proxies"`
		} `yaml:"proxy-groups"`
		Proxies []struct {
			Name string `yaml:"name"`
		} `yaml:"proxies"`
		Rules []string `yaml:"rules"`
	}
	if err := yaml.Unmarshal(rendered.Body, &config); err != nil {
		return err
	}
	result := personalTemplateDraftResult{Format: rendered.Format, Body: string(rendered.Body), Groups: []templatePreviewGroup{}, Nodes: []templatePreviewNode{}, Warnings: []string{}}
	policies := map[string]bool{"DIRECT": true, "REJECT": true, "REJECT-DROP": true, "PASS": true, "GLOBAL": true, "COMPATIBLE": true}
	for _, g := range config.Groups {
		result.Groups = append(result.Groups, templatePreviewGroup{Name: g.Name, Members: g.Members})
		policies[g.Name] = true
	}
	generated := map[string]bool{}
	for _, p := range config.Proxies {
		generated[p.Name] = true
		policies[p.Name] = true
	}
	for i, rule := range config.Rules {
		parts := strings.Split(rule, ",")
		if len(parts) < 2 {
			continue
		}
		typ := strings.ToUpper(strings.TrimSpace(parts[0]))
		switch typ {
		case "DOMAIN", "DOMAIN-SUFFIX", "DOMAIN-KEYWORD", "IP-CIDR", "IP-CIDR6", "GEOIP", "GEOSITE", "PROCESS-NAME", "PROCESS-PATH", "RULE-SET", "DST-PORT", "SRC-IP-CIDR", "MATCH":
		default:
			continue
		}
		index := len(parts) - 1
		if strings.EqualFold(strings.TrimSpace(parts[index]), "no-resolve") {
			index--
		}
		if index > 0 && !policies[strings.TrimSpace(parts[index])] {
			result.Warnings = append(result.Warnings, fmt.Sprintf("第 %d 条规则引用的策略 %q 不在本次生成的节点或代理组中", i+1, strings.TrimSpace(parts[index])))
		}
	}
	for _, p := range b.Proxies {
		if generated[p.Name] {
			result.Nodes = append(result.Nodes, templatePreviewNode{Name: p.Name, Region: p.Region, Source: p.RegionSource})
		}
	}
	for _, c := range b.Chains {
		if generated[c.Proxy.Name] {
			result.Nodes = append(result.Nodes, templatePreviewNode{Name: c.Proxy.Name, Region: c.Proxy.Region, Source: c.Proxy.RegionSource})
		}
	}
	httpx.OK(w, result)
	return nil
}

func (a *API) setSharePersonalTemplate(w http.ResponseWriter, r *http.Request) error {
	sh, err := a.ownShare(r)
	if err != nil {
		return err
	}
	var in personalTemplateSelection
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	format := subscription.NormalizeFormat(r.PathValue("format"))
	_, validatedAt, err := a.previewPersonalTemplate(r, sh, in.TemplateID, format)
	if err != nil {
		return err
	}
	if err := a.Store.SetSharePersonalTemplate(r.Context(), sh.ID, *sh.UserID, in.TemplateID, format, validatedAt); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return httpx.Conflict("分享归属或模板已变化，请刷新后重试")
		}
		return err
	}
	_ = a.audit(r, "personal_template.apply", strconv.FormatInt(sh.ID, 10), map[string]any{"template_id": in.TemplateID, "format": format})
	httpx.OK(w, map[string]any{"format": format, "template_id": in.TemplateID})
	return nil
}

func (a *API) clearSharePersonalTemplate(w http.ResponseWriter, r *http.Request) error {
	sh, err := a.ownShare(r)
	if err != nil {
		return err
	}
	format := subscription.NormalizeFormat(r.PathValue("format"))
	if format != "mihomo" && format != "surge" && format != "shadowrocket" && format != "singbox" {
		return httpx.BadRequest("模板格式无效")
	}
	if err := a.Store.ClearSharePersonalTemplate(r.Context(), sh.ID, *sh.UserID, format); err != nil {
		return err
	}
	_ = a.audit(r, "personal_template.clear", strconv.FormatInt(sh.ID, 10), map[string]any{"format": format})
	httpx.NoContent(w)
	return nil
}
