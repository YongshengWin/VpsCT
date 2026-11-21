import * as React from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, Trash2, Pencil, Copy, History } from "lucide-react";
import { del, get, post, put } from "@/lib/api";
import type { RuleTemplate, Preset, ProxyGroup, Share } from "@/lib/types";
import { copyText, fmtDate } from "@/lib/utils";
import { buildTemplatePrompt, TEMPLATE_GUIDES } from "@/lib/template-prompt";
import { TemplatePromptCard, TemplateFormatHelp } from "@/components/template-helper";
import { Badge, Button, Card, Confirm, Dialog, Empty, Field, Input, PageHeader, Select, Spinner, Tabs, Textarea, Code } from "@/components/ui";
import { useToast } from "@/components/toast";
import { GroupEditor, RulesHint } from "@/components/group-editor";
import { useIsAdmin, useProductPermissions } from "@/lib/auth";

export function TemplatesPage() {
  const [tab, setTab] = React.useState<"templates" | "presets">("templates");
  const admin = useIsAdmin();
  return (
    <div>
      <PageHeader title="模板与预设" description="配置模板决定客户端的完整配置；分组与规则预设用于复用订阅中的选路设置。" />
      {admin && <Tabs value={tab} onChange={setTab} items={[{ value: "templates", label: "配置模板" }, { value: "presets", label: "分组与规则预设" }]} />}
      <div className="mt-4">{tab === "templates" ? <TemplateList /> : <PresetList />}</div>
    </div>
  );
}

// ---------- templates ----------
function TemplateList() {
  const qc = useQueryClient();
  const toast = useToast();
  const permissions = useProductPermissions();
  const q = useQuery({ queryKey: ["templates"], queryFn: () => get<RuleTemplate[]>("/api/v1/templates") });
  const [edit, setEdit] = React.useState<RuleTemplate | null | "new">(null);
  const [confirmDel, setConfirmDel] = React.useState<RuleTemplate | null>(null);
  const [history, setHistory] = React.useState<RuleTemplate | null>(null);
  const delM = useMutation({ mutationFn: (id: number) => del(`/api/v1/templates/${id}`), onSuccess: () => { toast.success("已删除"); setConfirmDel(null); qc.invalidateQueries({ queryKey: ["templates"] }); }, onError: (e) => toast.fromError(e) });
  const dup = (t: RuleTemplate) => setEdit({ ...t, id: 0, name: `${t.name} 副本`, is_builtin: false });
  return (
    <div>
      {permissions.can("ai.access") && <TemplatePromptCard />}
      {permissions.can("templates.create") && <div className="mb-3 flex justify-end"><Button onClick={() => setEdit("new")}><Plus className="h-4 w-4" /> 新建模板</Button></div>}
      {q.isLoading ? <Spinner /> : !q.data?.length ? <Empty title="没有模板" /> : (
        <div className="grid gap-3 md:grid-cols-2">
          {q.data.map((t) => (
            <Card key={t.id} className="p-4">
              <div className="flex items-start justify-between gap-2">
                <div className="min-w-0">
                  <p className="flex items-center gap-2 font-medium"><span className="wrap-break-word">{t.name}</span><Badge variant="outline">{t.kind}</Badge>{t.is_builtin && <Badge variant="secondary">内置</Badge>}</p>
                  <p className="mt-1 text-xs text-muted-foreground">{t.description || "无描述"} · {t.content.split("\n").length} 行</p>
                </div>
                <div className="flex shrink-0">
                  {permissions.can("templates.create") && <Button size="icon" variant="ghost" title="历史版本" onClick={() => setHistory(t)}><History className="h-4 w-4" /></Button>}
                  {permissions.can("templates.create") && <Button size="icon" variant="ghost" title="复制为新模板" onClick={() => dup(t)}><Copy className="h-4 w-4" /></Button>}
                  {permissions.can("templates.edit") && <Button size="icon" variant="ghost" title="编辑" onClick={() => setEdit(t)}><Pencil className="h-4 w-4" /></Button>}
                  {!t.is_builtin && permissions.can("templates.delete") && <Button size="icon" variant="ghost" className="text-red-500" onClick={() => setConfirmDel(t)}><Trash2 className="h-4 w-4" /></Button>}
                </div>
              </div>
            </Card>
          ))}
        </div>
      )}
      <TemplateDialog t={edit} onClose={() => setEdit(null)} />
      <TemplateHistoryDialog t={history} onClose={() => setHistory(null)} onCopy={version => { setHistory(null); setEdit({ ...version, id: 0, name: `${version.name} 历史副本`, is_builtin: false }); }} />
      <Confirm open={!!confirmDel} onClose={() => setConfirmDel(null)} onConfirm={() => confirmDel && delM.mutate(confirmDel.id)} loading={delM.isPending} destructive title={`删除模板「${confirmDel?.name}」？`} description="使用该模板的订阅将回退到内置默认模板。" />
    </div>
  );
}

type TemplateVersion = RuleTemplate & { version_id: number; updated_at: string };
export function TemplateHistoryDialog({ t, personal = false, onClose, onCopy }: { t: RuleTemplate | null; personal?: boolean; onClose: () => void; onCopy: (v: RuleTemplate) => void }) {
  const q = useQuery({ queryKey: ["template-versions", personal, t?.id], enabled: !!t, queryFn: () => get<TemplateVersion[]>(`${personal ? "/api/v1/me/templates" : "/api/v1/templates"}/${t!.id}/versions`) });
  return <Dialog open={!!t} onClose={onClose} title={`历史版本：${t?.name ?? ""}`} description="每次修改前保留旧版本，最多 30 个。可从历史版本创建副本，预览后再应用。" footer={<Button variant="outline" onClick={onClose}>关闭</Button>}>{q.isLoading ? <Spinner /> : q.isError ? <p className="text-sm text-destructive">历史版本加载失败。</p> : !q.data?.length ? <Empty title="暂无历史版本" description="模板下次修改时会保留当前版本。" /> : <div className="space-y-2">{q.data.map(v => <div key={v.version_id} className="flex items-center justify-between gap-3 rounded-xl border p-3 text-sm"><div><p>{v.name}</p><p className="text-xs text-muted-foreground">保存于 {fmtDate(v.updated_at)} · {v.kind}</p></div><Button size="sm" variant="outline" onClick={() => onCopy(v)}>从此版本创建</Button></div>)}</div>}</Dialog>;
}

export function TemplateDialog({ t, onClose, personal = false, shareID }: { t: RuleTemplate | null | "new"; onClose: () => void; personal?: boolean; shareID?: number }) {
  const qc = useQueryClient();
  const toast = useToast();
  const admin = useIsAdmin();
  const permissions = useProductPermissions();
  const isNew = t === "new" || (t && t.id === 0);
  const [f, setF] = React.useState({ name: "", kind: "mihomo" as RuleTemplate["kind"], description: "", content: "" });
  const [busy, setBusy] = React.useState(false);
  const [loadError, setLoadError] = React.useState("");
  const [preview, setPreview] = React.useState<{ body: string; groups: { name: string; members: string[] }[]; nodes: { name: string; region: string; source: string }[]; warnings: string[] } | null>(null);
  const [previewShare, setPreviewShare] = React.useState("");
  const shares = useQuery({ queryKey: ["template-preview-shares"], enabled: !!t && admin && !personal && f.kind === "mihomo", queryFn: () => get<Share[]>("/api/v1/shares") });
  const targetShare = personal ? shareID : Number(previewShare) || undefined;
  const drafts = React.useRef<Partial<Record<RuleTemplate["kind"], string>>>({});
  React.useEffect(() => {
    let cancelled = false;
    if (!t) return () => { cancelled = true; };
    drafts.current = {}; setLoadError(""); setPreview(null); setPreviewShare(""); setBusy(true);
    void (async () => {
      try {
        let initial = t === "new" ? { name: "", kind: "mihomo" as const, description: "", content: "" } : { name: t.name, kind: t.kind, description: t.description, content: t.content };
        if (t === "new") {
          const defaults = qc.getQueryData<RuleTemplate[]>(["templates"]) ?? await get<RuleTemplate[]>("/api/v1/templates");
          initial = { ...initial, content: defaults.find(x => x.kind === "mihomo" && x.is_builtin)?.content ?? "" };
        }
        if (cancelled) return;
        setF(initial);
      } catch (e) { if (!cancelled) { setF({ name: "", kind: "mihomo", description: "", content: "" }); setLoadError(e instanceof Error ? e.message : "加载模板失败"); } }
      finally { if (!cancelled) setBusy(false); }
    })();
    return () => { cancelled = true; };
  }, [t, qc]);
  const changeKind = async (kind: RuleTemplate["kind"]) => {
    if (kind === f.kind || busy) return;
    setBusy(true); setLoadError(""); setPreview(null);
    try {
      drafts.current[f.kind] = f.content;
      let content = drafts.current[kind];
      if (content === undefined) {
        const defaults = qc.getQueryData<RuleTemplate[]>(["templates"]) ?? await get<RuleTemplate[]>("/api/v1/templates");
        content = defaults.find(x => x.kind === kind && x.is_builtin)?.content ?? "";
      }
      setF(old => ({ ...old, kind, content }));
    } catch (e) { setLoadError(e instanceof Error ? e.message : "加载默认模板失败"); }
    finally { setBusy(false); }
  };
  const m = useMutation({
    mutationFn: async () => {
      const base = personal ? "/api/v1/me/templates" : "/api/v1/templates";
      return isNew ? post<RuleTemplate>(base, f) : put<RuleTemplate>(`${base}/${(t as RuleTemplate).id}`, f);
    },
    onSuccess: () => { toast.success("已保存"); qc.invalidateQueries({ queryKey: [personal ? "personal-templates" : "templates"] }); onClose(); },
    onError: (e) => toast.fromError(e),
  });
  const builtin = t && t !== "new" && t.is_builtin;
  return (
    <Dialog open={!!t} onClose={onClose} wide title={isNew ? "新建模板" : builtin ? `编辑内置模板：${f.name}` : `编辑模板：${f.name}`} description={builtin ? "保存后将成为自定义模板（内置标记去除），可随时复制内置版本还原。" : TEMPLATE_GUIDES[f.kind].summary}
      footer={<><Button variant="outline" onClick={onClose}>取消</Button><Button onClick={() => m.mutate()} loading={m.isPending} disabled={busy}>保存</Button></>}>
      <div className="grid gap-3 sm:grid-cols-3">
        <Field label="名称" hint="在面板里识别这份模板"><Input aria-label="模板名称" value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} /></Field>
        <Field label="格式" hint="每份模板只对应一个客户端格式">
          <Select aria-label="模板格式" value={f.kind} onChange={(e) => void changeKind(e.target.value as RuleTemplate["kind"])} disabled={!isNew || busy}>
            <option value="mihomo">Clash / mihomo (YAML)</option><option value="surge">Surge</option><option value="shadowrocket">Shadowrocket</option><option value="singbox">sing-box (JSON)</option>
          </Select>
        </Field>
        <Field label="描述" hint="记录用途或与默认配置的差别"><Input aria-label="模板描述" value={f.description} onChange={(e) => setF({ ...f, description: e.target.value })} /></Field>
      </div>
      <div className="mt-3 space-y-3">
        <TemplateFormatHelp kind={f.kind} />
        {loadError && <p role="alert" className="text-sm text-destructive">{loadError}</p>}
        {busy ? <Spinner /> : <Field label="模板正文" hint="只粘贴配置正文，不包含 Markdown 代码围栏、名称或说明文字。">
          <Textarea aria-label="模板正文" className="mono" rows={18} value={f.content} onChange={(e) => setF({ ...f, content: e.target.value })} spellCheck={false} />
        </Field>}
        {f.kind === "mihomo" && (personal || admin) && <div className="space-y-2">{!personal && <Field label="预览使用的分享" hint="选择分享后可用其实际节点检查草稿；预览不会修改分享。"><Select aria-label="预览使用的分享" value={previewShare} onChange={e => { setPreviewShare(e.target.value); setPreview(null); }}><option value="">选择分享</option>{shares.data?.map(s => <option key={s.id} value={s.id}>{s.name}</option>)}</Select></Field>}<Button type="button" size="sm" variant="outline" disabled={busy || !targetShare} onClick={async () => { setBusy(true); setPreview(null); try { const content = f.content; const path = personal ? "personal-templates/preview-draft" : "template-preview-draft"; const result = await post<NonNullable<typeof preview>>(`/api/v1/shares/${targetShare}/${path}`, { content }); setPreview(result); } catch (e) { toast.fromError(e); } finally { setBusy(false); } }}>预览当前配置</Button>{preview && <div className="space-y-2"><p className="text-xs text-muted-foreground">使用所选分享的节点生成；配置更改后需重新预览。</p><details open><summary className="cursor-pointer text-sm font-medium">地区识别 ({preview.nodes.length} 个节点)</summary><div className="mt-2 space-y-1 text-xs">{preview.nodes.length ? preview.nodes.map((n, i) => <p key={i}>{n.name} · {n.region || "未分类"} · {({ manual: "手动设置", server: "服务器地区", name: "节点名称", ambiguous: "名称包含多个地区", egress_unknown: "出口地区未确认", unknown: "无法识别" } as Record<string, string>)[n.source] ?? "无法识别"}</p>) : <p>当前分享没有可用节点。</p>}</div></details><details><summary className="cursor-pointer text-sm font-medium">生成的代理组 ({preview.groups.length})</summary><div className="mt-2 max-h-52 space-y-2 overflow-auto text-xs">{preview.groups.map((g, i) => <p key={i}><strong>{g.name}</strong>：{g.members.join("、") || "无成员"}</p>)}</div></details><details><summary className="cursor-pointer text-sm font-medium">完整配置（含连接信息）</summary><p className="text-xs text-muted-foreground">配置包含连接信息，请妥善保管。</p><pre className="mt-2 max-h-72 overflow-auto whitespace-pre-wrap break-all rounded-xl bg-muted p-3 text-xs">{preview.body}</pre></details></div>}</div>}
        {preview && preview.warnings.length > 0 && <div role="alert" className="rounded-xl border border-amber-300 p-3 text-sm text-amber-700"><p className="font-medium">预览发现需要检查的问题</p>{preview.warnings.map((warning, i) => <p key={i}>{warning}</p>)}</div>}
        {permissions.can("ai.access") && <Button size="sm" variant="outline" disabled={busy} onClick={async () => { setBusy(true); try { const content = f.content; await copyText(buildTemplatePrompt({ kind: f.kind, task: "adapt", source: content })); toast.success("已复制本模板的 AI 改造指令"); } catch (e) { toast.fromError(e); } finally { setBusy(false); } }}><Copy className="h-4 w-4" /> 复制本模板的 AI 改造指令</Button>}
        <p className="text-xs leading-5 text-muted-foreground">保存会做空节点试渲染。YAML / JSON 会检查可解析性；Surge / 小火箭不做完整客户端语法校验。请继续在订阅预览和实际客户端中检查分组、分流与 DNS。</p>
      </div>
    </Dialog>
  );
}

// ---------- presets ----------
function PresetList() {
  const qc = useQueryClient();
  const toast = useToast();
  const q = useQuery({ queryKey: ["presets"], queryFn: () => get<Preset[]>("/api/v1/presets") });
  const [edit, setEdit] = React.useState<Preset | null | "new">(null);
  const [confirmDel, setConfirmDel] = React.useState<Preset | null>(null);
  const delM = useMutation({ mutationFn: (id: number) => del(`/api/v1/presets/${id}`), onSuccess: () => { toast.success("已删除"); setConfirmDel(null); qc.invalidateQueries({ queryKey: ["presets"] }); }, onError: (e) => toast.fromError(e) });
  return (
    <div>
      <Card className="mb-4 space-y-4 p-4 text-sm leading-6 text-muted-foreground">
        <div>
          <h2 className="font-semibold text-foreground">预设 = 一套可重复使用的分组和分流规则</h2>
          <p>例如保存「手动选择 + 自动测速，国内直连，其余走自动选择」。以后给不同订阅用时，整套填入即可，不必重新建组、写规则。</p>
        </div>
        <div className="grid gap-3 md:grid-cols-3">
          <div className="rounded-xl bg-muted/50 p-3"><p className="font-medium text-foreground">分组：怎么选节点</p><p>「自动选择」挑延迟较低的节点；「手动选择」让你在客户端自行切换。地区筛选按节点名称匹配。</p></div>
          <div className="rounded-xl bg-muted/50 p-3"><p className="font-medium text-foreground">规则：流量交给谁</p><p><Code>GEOIP,CN,DIRECT</Code> 表示国内 IP 直连；<Code>MATCH,自动选择</Code> 把其余流量交给「自动选择」组。</p></div>
          <div className="rounded-xl bg-muted/50 p-3"><p className="font-medium text-foreground">模板：完整配置</p><p>DNS、TUN、监听等继续由模板提供。如果模板里的分组和分流已经合适，就无需再套预设。</p></div>
        </div>
        <ol className="list-decimal space-y-1 pl-5">
          <li>在这里新建或复制预设，保存分组与规则。</li>
          <li>打开「订阅链接 → 编辑订阅 → 代理组 / 规则与模板」，点击「套用分组与规则」。</li>
          <li>预览后保存订阅，客户端下次更新时获取生成结果。分享只使用模板，不套用预设。</li>
        </ol>
        <p className="rounded-xl border p-3">套用会把分组与规则复制到当前订阅。之后修改预设，不会自动修改已经套用的订阅；需要重新套用并保存。动态节点筛选会在每次生成订阅时重新计算。</p>
        <details><summary className="cursor-pointer font-medium text-foreground">套用后，模板里的规则会怎样？</summary><p className="mt-2">Clash 会用自定义规则替换模板规则；Surge / 小火箭按模板的规则插入位置合并，保留模板的最终规则；sing-box 会把转换后的规则追加到模板规则之后。请在对应格式的预览中检查，尤其是模板中已写了提前匹配的规则时。</p></details>
      </Card>
      <div className="mb-3 flex justify-end"><Button onClick={() => setEdit("new")}><Plus className="h-4 w-4" /> 新建分组与规则预设</Button></div>
      {q.isLoading ? <Spinner /> : !q.data?.length ? <Empty title="还没有预设" description="如果模板的分组和规则已经够用，可以不创建预设。" /> : (
        <div className="grid gap-3 md:grid-cols-2">
          {q.data.map((p) => (
            <Card key={p.id} className="p-4">
              <div className="flex items-start justify-between gap-2">
                <div className="min-w-0">
                  <p className="flex items-center gap-2 font-medium"><span className="wrap-break-word">{p.name}</span>{p.is_builtin && <Badge variant="secondary">内置</Badge>}</p>
                  <p className="mt-1 text-xs text-muted-foreground">{p.groups.length} 个代理组 · {p.rules.length} 条规则</p>
                  <p className="mt-1 flex flex-wrap gap-1">{p.groups.map((g) => <Code key={g.name}>{g.name}</Code>)}</p>
                </div>
                <div className="flex shrink-0">
                  <Button size="icon" variant="ghost" title="复制为新预设" onClick={() => setEdit({ ...p, id: 0, name: `${p.name} 副本`, is_builtin: false })}><Copy className="h-4 w-4" /></Button>
                  <Button size="icon" variant="ghost" title="编辑预设" onClick={() => setEdit(p)}><Pencil className="h-4 w-4" /></Button>
                  {!p.is_builtin && <Button size="icon" variant="ghost" className="text-red-500" title="删除预设" onClick={() => setConfirmDel(p)}><Trash2 className="h-4 w-4" /></Button>}
                </div>
              </div>
            </Card>
          ))}
        </div>
      )}
      <PresetDialog p={edit} onClose={() => setEdit(null)} />
      <Confirm open={!!confirmDel} onClose={() => setConfirmDel(null)} onConfirm={() => confirmDel && delM.mutate(confirmDel.id)} loading={delM.isPending} destructive title={`删除预设「${confirmDel?.name}」？`} />
    </div>
  );
}

function PresetDialog({ p, onClose }: { p: Preset | null | "new"; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const isNew = p === "new" || (p && p.id === 0);
  const [name, setName] = React.useState("");
  const [groups, setGroups] = React.useState<ProxyGroup[]>([]);
  const [rules, setRules] = React.useState("");
  const [tab, setTab] = React.useState<"groups" | "rules">("groups");
  React.useEffect(() => {
    if (p && p !== "new") { setName(p.name); setGroups(p.groups); setRules(p.rules.join("\n")); }
    else if (p === "new") { setName(""); setGroups([]); setRules(""); }
    setTab("groups");
  }, [p]);
  const m = useMutation({
    mutationFn: () => {
      const body = { name, groups, rules: rules.split("\n").map((l) => l.trim()).filter(Boolean) };
      return isNew ? post<Preset>("/api/v1/presets", body) : put<Preset>(`/api/v1/presets/${(p as Preset).id}`, body);
    },
    onSuccess: () => { toast.success("已保存"); qc.invalidateQueries({ queryKey: ["presets"] }); onClose(); },
    onError: (e) => toast.fromError(e),
  });
  return (
    <Dialog open={!!p} onClose={onClose} fill size="xl" title={isNew ? "新建分组与规则预设" : `编辑预设：${name}`} description="保存一套分组与规则，供多个订阅重复使用。这里不选择实际服务器；每次生成订阅时，才从该订阅已选节点中匹配成员。保存预设不会自动应用到订阅。"
      footer={<><Button variant="outline" onClick={onClose}>取消</Button><Button onClick={() => m.mutate()} loading={m.isPending}>保存</Button></>}>
      <div className="flex min-h-0 flex-1 flex-col">
        <div className="mb-3 flex shrink-0 flex-wrap items-end gap-3">
          <Field label="名称" hint="例如：手动 + 自动，国内直连" className="min-w-0 flex-1 sm:min-w-48"><Input aria-label="预设名称" value={name} onChange={(e) => setName(e.target.value)} /></Field>
          <Tabs value={tab} onChange={setTab} items={[{ value: "groups", label: `代理组 (${groups.length})` }, { value: "rules", label: `规则 (${rules.split("\n").filter(Boolean).length})` }]} />
        </div>
        {tab === "groups" ? (
          <GroupEditor className="min-h-0 flex-1" variant="preset" groups={groups} onChange={setGroups} nodes={[]} />
        ) : (
          <div className="min-h-0 flex-1 overflow-y-auto">
            <p className="mb-2 text-sm leading-6 text-muted-foreground">每行一条规则，按顺序匹配。最后的策略名必须是左侧创建的组名，或 DIRECT / REJECT。例如 MATCH,自动选择 要求存在「自动选择」组。</p>
            <Textarea aria-label="预设分流规则" className="mono" rows={16} value={rules} onChange={(e) => setRules(e.target.value)} spellCheck={false} />
            <div className="mt-2"><RulesHint /></div>
          </div>
        )}
      </div>
    </Dialog>
  );
}
