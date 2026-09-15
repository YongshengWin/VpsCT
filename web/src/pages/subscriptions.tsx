import * as React from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Copy, Eye, KeyRound, Pencil, Plus, QrCode, Trash2, History, Wand2, Save, RefreshCw } from "lucide-react";
import { del, get, post, put } from "@/lib/api";
import { normalizeGroups, type Subscription, type Node, type ExternalSubscription, type RuleTemplate, type Preset, type ProxyGroup, type ChainSpec, type NodeSelection, type User, type AccessLog } from "@/lib/types";
import { copyText, displayName, fmtBytes, fmtDate, fmtAgo, gbToBytes, bytesToGb, parseResetDay, formatResetDay, FORMAT_LABELS, PROTOCOL_LABELS, cn } from "@/lib/utils";
import { Badge, Button, Card, Confirm, Dialog, Empty, Field, Input, PageHeader, Select, Spinner, Switch, Table, Td, Th, Tr, Textarea, Pre, Tabs, Code } from "@/components/ui";
import { DateTimeInput, ResetDayInput } from "@/components/datetime-picker";
import { useToast } from "@/components/toast";
import { useAuth, useIsAdmin } from "@/lib/auth";
import { GroupEditor, RulesHint, type Candidate } from "@/components/group-editor";
import { QR } from "@/pages/nodes";

const KIND_LABELS: Record<Subscription["kind"], string> = { generated: "生成", imported: "转换导入", share: "分享" };

function SubName({ sub }: { sub: Subscription }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [editing, setEditing] = React.useState(false);
  const [name, setName] = React.useState(sub.name);
  React.useEffect(() => setName(sub.name), [sub.name]);
  const save = useMutation({
    mutationFn: (next: string) => put<Subscription>(`/api/v1/subscriptions/${sub.id}`, { name: next }),
    onSuccess: () => {
      toast.success("已更名");
      setEditing(false);
      qc.invalidateQueries({ queryKey: ["subscriptions"] });
      qc.invalidateQueries({ queryKey: ["shares"] });
    },
    onError: (e) => toast.fromError(e),
  });
  const commit = () => {
    const next = name.trim();
    if (!next || next === sub.name) {
      setName(sub.name);
      setEditing(false);
      return;
    }
    save.mutate(next);
  };
  if (!editing) {
    return (
      <button type="button" className="break-words text-left font-medium hover:underline" title="点击更名" onClick={() => setEditing(true)}>
        {sub.name}
      </button>
    );
  }
  return (
    <input
      autoFocus
      className="h-8 w-full min-w-0 rounded-lg border border-primary/40 bg-card px-2 text-sm font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/25"
      value={name}
      maxLength={64}
      disabled={save.isPending}
      onChange={(e) => setName(e.target.value)}
      onBlur={commit}
      onKeyDown={(e) => {
        if (e.key === "Enter") {
          e.preventDefault();
          commit();
        }
        if (e.key === "Escape") {
          setName(sub.name);
          setEditing(false);
        }
      }}
    />
  );
}

// ---------- links widget (also used on dashboard) ----------
export function SubscriptionLinks({ sub, compact }: { sub: Subscription; compact?: boolean }) {
  const toast = useToast();
  const { meta } = useAuth();
  const [qr, setQr] = React.useState<string | null>(null);
  const formats = (meta?.formats ?? ["mihomo", "surge", "singbox", "raw"]).filter((f) => sub.links?.[f]);
  const copy = async (url: string, label: string) => { await copyText(url); toast.success(`已复制 ${label} 链接`); };
  if (sub.supported === false) return <p className="mt-2 text-sm text-muted-foreground">此订阅类型已停用，请从节点库重新生成订阅并选择模板。</p>;
  if (!sub.links || !Object.keys(sub.links).length) return <p className="text-xs text-muted-foreground">该订阅未启用或无权限查看链接</p>;
  return (
    <div className={cn("mt-2", compact ? "space-y-1" : "space-y-2")}>
      <div className="flex flex-wrap items-center gap-1.5">
        <Badge variant="outline" className="cursor-pointer hover:bg-accent" onClick={() => copy(sub.links.auto, "自动识别")}><Copy className="mr-1 h-3 w-3" /> 自动识别</Badge>
        {formats.map((f) => (
          <Badge key={f} variant="secondary" className="cursor-pointer hover:bg-accent" onClick={() => copy(sub.links[f], FORMAT_LABELS[f] ?? f)}>{FORMAT_LABELS[f] ?? f}</Badge>
        ))}
        <button className="rounded p-1 text-muted-foreground hover:bg-accent hover:text-foreground" title="二维码" onClick={() => setQr(qr ? null : sub.links.auto)}><QrCode className="h-4 w-4" /></button>
      </div>
      {!compact && (
        <>
          <p className="mono break-all text-xs text-muted-foreground">{sub.short_link || sub.links.auto}</p>
          <p className="text-xs text-muted-foreground">v2rayN / v2rayU 请复制「Base64」链接；用自动识别时它们常拿到 Clash YAML，VLESS Reality 会连不上。</p>
        </>
      )}
      {qr && <QR text={qr} className="mx-0" />}
    </div>
  );
}

// ---------- list ----------
export function SubscriptionsPage() {
  const admin = useIsAdmin();
  const qc = useQueryClient();
  const toast = useToast();
  const q = useQuery({ queryKey: ["subscriptions"], queryFn: () => get<Subscription[]>("/api/v1/subscriptions") });
  const [confirmDel, setConfirmDel] = React.useState<Subscription | null>(null);
  const [confirmRotate, setConfirmRotate] = React.useState<Subscription | null>(null);
  const [preview, setPreview] = React.useState<Subscription | null>(null);
  const [logs, setLogs] = React.useState<Subscription | null>(null);
  const delM = useMutation({ mutationFn: (id: number) => del(`/api/v1/subscriptions/${id}`), onSuccess: () => { toast.success("已删除"); setConfirmDel(null); qc.invalidateQueries({ queryKey: ["subscriptions"] }); }, onError: (e) => toast.fromError(e) });
  const rotate = useMutation({ mutationFn: (id: number) => post(`/api/v1/subscriptions/${id}/rotate-token`), onSuccess: () => { toast.success("链接已更换，旧链接立即失效"); setConfirmRotate(null); qc.invalidateQueries({ queryKey: ["subscriptions"] }); }, onError: (e) => toast.fromError(e) });
  const toggle = useMutation({ mutationFn: (s: Subscription) => put(`/api/v1/subscriptions/${s.id}`, { enabled: !s.enabled }), onSuccess: () => qc.invalidateQueries({ queryKey: ["subscriptions"] }), onError: (e) => toast.fromError(e) });
  return (
    <div>
      <PageHeader title="订阅链接" description="点击格式标签即可复制链接" />
      {admin && (
        <div className="mb-4 flex flex-wrap gap-2">
          <Link to="/subscriptions/new"><Button><Plus className="h-4 w-4" /> 生成订阅</Button></Link>
          <Link to="/subscriptions/new?kind=imported"><Button variant="outline">转换外部订阅</Button></Link>
        </div>
      )}
      {q.isLoading ? <Spinner /> : !q.data?.length ? (
        <Empty title="还没有订阅" description={admin ? "从节点库选择节点并套用模板，或转换外部订阅。" : "管理员尚未给你分配订阅。"} action={admin && <Link to="/subscriptions/new"><Button>生成订阅</Button></Link>} />
      ) : (
        <div className="grid gap-3 md:grid-cols-2">
          {q.data.map((s) => (
            <Card key={s.id} className="p-4">
              <div className="flex items-start justify-between gap-2">
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2">
                    {admin && s.supported !== false ? <SubName sub={s} /> : <p className="break-words font-medium">{s.name}</p>}
                    <Badge variant={s.kind === "share" ? "info" : "secondary"}>{KIND_LABELS[s.kind] ?? "已停用类型"}{s.share_name ? ` · ${s.share_name}` : ""}</Badge>
                    {!s.enabled && <Badge variant="destructive">已停用</Badge>}
                  </div>
                  <p className="mt-1 text-xs text-muted-foreground">
                    {s.node_count} 节点 · 默认 {FORMAT_LABELS[s.default_format] ?? s.default_format} · 访问 {s.access_count} 次 · 最近 {fmtAgo(s.last_access_at)}
                    {s.expire_at && ` · 到期 ${fmtDate(s.expire_at, false)}`}
                    {s.traffic_limit_bytes > 0 && ` · 限额 ${fmtBytes(s.traffic_limit_bytes)}`}
                    {s.reset_day > 0 && ` · ${formatResetDay(s.reset_day)}重置${s.next_reset ? `（下次 ${fmtDate(s.next_reset, false)}）` : ""}`}
                  </p>
                </div>
                {admin && (
                  <div className="flex shrink-0 items-center">
                    {s.supported !== false && <Button size="icon" variant="ghost" title="预览" onClick={() => setPreview(s)}><Eye className="h-4 w-4" /></Button>}
                    <Button size="icon" variant="ghost" title="访问记录" onClick={() => setLogs(s)}><History className="h-4 w-4" /></Button>
                    {s.supported !== false && s.kind !== "share" && <Link to={`/subscriptions/${s.id}/edit`}><Button size="icon" variant="ghost" title="编辑"><Pencil className="h-4 w-4" /></Button></Link>}
                    {s.supported !== false && <Button size="icon" variant="ghost" title="更换链接" onClick={() => setConfirmRotate(s)}><KeyRound className="h-4 w-4" /></Button>}
                    {s.kind !== "share" && <Button size="icon" variant="ghost" className="text-red-500" title="删除" onClick={() => setConfirmDel(s)}><Trash2 className="h-4 w-4" /></Button>}
                  </div>
                )}
              </div>
              <SubscriptionLinks sub={s} />
              {admin && s.supported !== false && s.kind !== "share" && (
                <div className="mt-3 flex items-center justify-between border-t pt-2">
                  <Switch checked={s.enabled} onChange={() => toggle.mutate(s)} label="启用" />
                  {s.allowed_user_ids?.length ? <span className="text-xs text-muted-foreground">已授权 {s.allowed_user_ids.length} 个用户</span> : null}
                </div>
              )}
            </Card>
          ))}
        </div>
      )}
      <Confirm open={!!confirmDel} onClose={() => setConfirmDel(null)} onConfirm={() => confirmDel && delM.mutate(confirmDel.id)} loading={delM.isPending} destructive title={`删除「${confirmDel?.name}」？`} description="订阅链接将立即失效。" />
      <Confirm open={!!confirmRotate} onClose={() => setConfirmRotate(null)} onConfirm={() => confirmRotate && rotate.mutate(confirmRotate.id)} loading={rotate.isPending} title="更换订阅链接？" description="会生成新的 token 和短链，已分发的旧链接立即失效。" />
      <PreviewDialog sub={preview} onClose={() => setPreview(null)} />
      <AccessLogDialog sub={logs} onClose={() => setLogs(null)} />
    </div>
  );
}

function PreviewDialog({ sub, onClose }: { sub: Subscription | null; onClose: () => void }) {
  const { meta } = useAuth();
  const [format, setFormat] = React.useState("mihomo");
  React.useEffect(() => { if (sub) setFormat(sub.default_format || "mihomo"); }, [sub]);
  const q = useQuery({ queryKey: ["subscriptions", sub?.id, "render", format], queryFn: () => get<{ body: string; node_count: number; content_type: string }>(`/api/v1/subscriptions/${sub!.id}/render?format=${format}`), enabled: !!sub });
  return (
    <Dialog open={!!sub} onClose={onClose} title={`预览：${sub?.name ?? ""}`} wide description={q.data ? `${q.data.node_count} 个节点 · ${q.data.content_type}` : undefined}>
      <Tabs className="mb-3" value={format} onChange={setFormat} items={(meta?.formats ?? ["mihomo", "surge", "singbox", "raw"]).map((f) => ({ value: f, label: FORMAT_LABELS[f] ?? f }))} />
      {q.isLoading ? <Spinner /> : q.error ? <p className="text-sm text-red-500">{String((q.error as Error).message)}</p> : <Pre className="max-h-[60vh] whitespace-pre-wrap break-all">{q.data?.body}</Pre>}
    </Dialog>
  );
}

function AccessLogDialog({ sub, onClose }: { sub: Subscription | null; onClose: () => void }) {
  const q = useQuery({ queryKey: ["subscriptions", sub?.id, "access-log"], queryFn: () => get<AccessLog[]>(`/api/v1/subscriptions/${sub!.id}/access-log?limit=200`), enabled: !!sub });
  return (
    <Dialog open={!!sub} onClose={onClose} title={`访问记录：${sub?.name ?? ""}`} wide description="客户端拉取订阅的记录（IP、UA、识别到的格式）">
      {q.isLoading ? <Spinner /> : !q.data?.length ? <p className="text-sm text-muted-foreground">尚无访问</p> : (
        <Table>
          <thead><tr className="border-b"><Th>时间</Th><Th>IP</Th><Th>格式</Th><Th>状态</Th><Th>User-Agent</Th></tr></thead>
          <tbody>{q.data.map((l) => <Tr key={l.id}><Td className="whitespace-nowrap text-xs">{fmtDate(l.ts)}</Td><Td className="mono text-xs">{l.ip}</Td><Td className="text-xs">{l.format}</Td><Td><Badge variant={l.status === 200 ? "success" : "destructive"}>{l.status}</Badge></Td><Td className="max-w-md break-all text-xs text-muted-foreground">{l.user_agent}</Td></Tr>)}</tbody>
        </Table>
      )}
    </Dialog>
  );
}

// ---------- editor ----------
interface EditorState {
  name: string;
  kind: Subscription["kind"];
  template_id: number;
  default_format: string;
  proxy_groups: ProxyGroup[];
  chains: ChainSpec[];
  rules: string[];
  node_selection: NodeSelection;
  source_external_id: number;
  expire_at: string;
  traffic_limit_gb: string;
  reset_day: string;
  userinfo_header: boolean;
  show_info_nodes: boolean;
  allowed_user_ids: number[];
  enabled: boolean;
  short_link: boolean;
}

const emptyState: EditorState = {
  name: "", kind: "generated", template_id: 0, default_format: "mihomo", proxy_groups: [], chains: [], rules: [],
  node_selection: { include_all: false, node_ids: [], external_sub_ids: [], tags: [] }, source_external_id: 0,
  expire_at: "", traffic_limit_gb: "", reset_day: "1", userinfo_header: true, show_info_nodes: true, allowed_user_ids: [], enabled: true, short_link: true,
};

function fromSub(s: Subscription): EditorState {
  return {
    name: s.name, kind: s.kind, template_id: s.template_id ?? 0, default_format: s.default_format, proxy_groups: normalizeGroups(s.proxy_groups), chains: s.chains ?? [], rules: s.rules ?? [],
    node_selection: { include_all: !!s.node_selection?.include_all, node_ids: s.node_selection?.node_ids ?? [], external_sub_ids: s.node_selection?.external_sub_ids ?? [], tags: s.node_selection?.tags ?? [], filter: s.node_selection?.filter, exclude_filter: s.node_selection?.exclude_filter },
    source_external_id: s.source_external_id ?? 0,
    expire_at: s.expire_at ? toLocalInput(s.expire_at) : "", traffic_limit_gb: bytesToGb(s.traffic_limit_bytes), reset_day: String(s.reset_day ?? 0), userinfo_header: s.userinfo_header, show_info_nodes: s.show_info_nodes,
    allowed_user_ids: s.allowed_user_ids ?? [], enabled: s.enabled, short_link: !!s.short_code,
  };
}

function toLocalInput(iso: string) {
  const d = new Date(iso);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function toPayload(s: EditorState) {
  return {
    name: s.name, kind: s.kind, template_id: s.template_id, default_format: s.default_format, proxy_groups: s.proxy_groups, chains: s.chains, rules: s.rules,
    node_selection: s.node_selection, source_external_id: s.kind === "imported" ? s.source_external_id || null : null,
    expire_at: s.expire_at ? new Date(s.expire_at).toISOString() : "0001-01-01T00:00:00Z", traffic_limit_bytes: gbToBytes(s.traffic_limit_gb), reset_day: parseResetDay(s.reset_day), userinfo_header: s.userinfo_header, show_info_nodes: s.show_info_nodes,
    allowed_user_ids: s.allowed_user_ids, enabled: s.enabled, short_link: s.short_link,
  };
}

export function SubscriptionEditorPage() {
  const { id } = useParams();
  const nav = useNavigate();
  const qc = useQueryClient();
  const toast = useToast();
  const { meta } = useAuth();
  const editing = !!id;
  const initialKind = new URLSearchParams(window.location.search).get("kind") as Subscription["kind"] | null;
  const existing = useQuery({ queryKey: ["subscriptions", id], queryFn: () => get<Subscription>(`/api/v1/subscriptions/${id}`), enabled: editing });
  const nodes = useQuery({ queryKey: ["nodes", { source: "" }], queryFn: () => get<Node[]>("/api/v1/nodes") });
  const externals = useQuery({ queryKey: ["externals"], queryFn: () => get<ExternalSubscription[]>("/api/v1/externals") });
  const templates = useQuery({ queryKey: ["templates"], queryFn: () => get<RuleTemplate[]>("/api/v1/templates") });
  const presets = useQuery({ queryKey: ["presets"], queryFn: () => get<Preset[]>("/api/v1/presets") });
  const users = useQuery({ queryKey: ["users"], queryFn: () => get<User[]>("/api/v1/users") });
  const [s, setS] = React.useState<EditorState>({ ...emptyState, kind: initialKind && ["generated", "imported"].includes(initialKind) ? initialKind : "generated" });
  const [tab, setTab] = React.useState<"basic" | "nodes" | "groups" | "rules" | "preview">("basic");
  const [loaded, setLoaded] = React.useState(!editing);
  React.useEffect(() => { if (existing.data && !loaded) { setS(fromSub(existing.data)); setLoaded(true); } }, [existing.data, loaded]);
  const set = <K extends keyof EditorState>(k: K, v: EditorState[K]) => setS((p) => ({ ...p, [k]: v }));
  const [restoreTemplate, setRestoreTemplate] = React.useState(false);
  const applyPreset = (p: Preset) => {
    setS((previous) => ({ ...previous, proxy_groups: normalizeGroups(p.groups), rules: [...(p.rules ?? [])] }));
    toast.success(`已填入「${p.name}」的分组与规则，保存订阅后生效`);
  };
  const setSel = (patch: Partial<NodeSelection>) => setS((p) => ({ ...p, node_selection: { ...p.node_selection, ...patch } }));

  const save = useMutation({
    mutationFn: () => (editing ? put<Subscription>(`/api/v1/subscriptions/${id}`, toPayload(s)) : post<Subscription>("/api/v1/subscriptions", toPayload(s))),
    onSuccess: (r) => { toast.success(editing ? "已保存" : "订阅已创建"); qc.invalidateQueries({ queryKey: ["subscriptions"] }); if (!editing) nav(`/subscriptions/${r.id}/edit`, { replace: true }); },
    onError: (e) => toast.fromError(e),
  });

  // selected nodes -> candidates for group editor
  const allNodes = (nodes.data ?? []).filter((n) => !n.share_id && !n.revoked);
  const selectedNodes = React.useMemo(() => {
    const sel = s.node_selection;
    let list = allNodes.filter((n) => n.enabled);
    if (!sel.include_all) {
      list = list.filter((n) => sel.node_ids.includes(n.id) || (n.external_sub_id != null && sel.external_sub_ids.includes(n.external_sub_id)) || (sel.tags?.length ? n.tags.some((t) => sel.tags!.includes(t)) : false));
    }
    const re = safeRe(sel.filter);
    const exre = safeRe(sel.exclude_filter);
    if (re) list = list.filter((n) => re.test(n.name));
    if (exre) list = list.filter((n) => !exre.test(n.name));
    return list;
  }, [allNodes, s.node_selection]);
  const candidates: Candidate[] = React.useMemo(() => [
    ...selectedNodes.map((n) => ({ name: n.name, kind: "node" as const, sub: PROTOCOL_LABELS[n.protocol] ?? n.protocol })),
    ...s.chains.filter((c) => c.name).map((c) => ({ name: c.name, kind: "chain" as const, sub: "链式" })),
  ], [selectedNodes, s.chains]);

  if (editing && existing.error) return <Empty title="无法加载订阅" description={existing.error.message} action={<Link to="/subscriptions"><Button>返回订阅列表</Button></Link>} />;
  if (editing && existing.data?.supported === false) return <Empty title="此订阅类型已停用" description="请从节点库重新生成订阅，并在规则与模板中选择配置模板。" action={<Link to="/subscriptions/new"><Button>生成订阅</Button></Link>} />;
  if (editing && (existing.isLoading || !loaded)) return <Spinner />;
  const allTags = Array.from(new Set(allNodes.flatMap((n) => n.tags))).sort();

  return (
    <div>
      <PageHeader
        back={{ to: "/subscriptions", label: "订阅链接" }}
        title={editing ? `编辑：${existing.data?.name ?? ""}` : s.kind === "imported" ? "转换外部订阅" : "生成订阅"}
        description={s.kind === "generated" ? "选择节点 → 编排代理组 → 写规则或套模板 → 预览并保存" : "把一个外部订阅换壳输出：应用你的模板与代理组，并可限制到期/用户"}
        actions={<><Button variant="outline" onClick={() => setTab("preview")}><Eye className="h-4 w-4" /> 预览</Button><Button onClick={() => save.mutate()} loading={save.isPending}><Save className="h-4 w-4" /> 保存</Button></>}
      />
      {editing && existing.data && <Card className="mb-4 p-4"><SubscriptionLinks sub={existing.data} /></Card>}
      <Tabs value={tab} onChange={setTab} items={[
        { value: "basic", label: "基本" },
        { value: "nodes", label: `节点选择 (${selectedNodes.length})` },
        { value: "groups", label: `代理组 (${s.proxy_groups.length})` },
        { value: "rules", label: "规则与模板" },
        { value: "preview", label: "预览" },
      ]} />

      <div className="mt-4">
        {tab === "basic" && (
          <Card className="p-4 sm:p-5">
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="名称"><Input value={s.name} onChange={(e) => set("name", e.target.value)} placeholder="我的订阅" /></Field>
              <Field label="类型">
                <Select value={s.kind} onChange={(e) => set("kind", e.target.value as Subscription["kind"])} disabled={editing}>
                  <option value="generated">生成（从节点库）</option>
                  <option value="imported">转换外部订阅</option>
                </Select>
              </Field>
              <Field label="默认格式" hint="/s/token 不带格式且无法识别 UA 时使用">
                <Select value={s.default_format} onChange={(e) => set("default_format", e.target.value)}>
                  {(meta?.formats ?? ["mihomo", "surge", "singbox", "raw", "uri", "shadowrocket"]).map((f) => <option key={f} value={f}>{FORMAT_LABELS[f] ?? f}</option>)}
                </Select>
              </Field>
              {s.kind === "imported" && (
                <Field label="来源订阅">
                  <Select value={String(s.source_external_id)} onChange={(e) => set("source_external_id", Number(e.target.value))}>
                    <option value="0">选择…</option>
                    {(externals.data ?? []).map((e) => <option key={e.id} value={e.id}>{e.name}（{e.node_count} 节点）</option>)}
                  </Select>
                </Field>
              )}
              <Field label="到期时间" hint="留空永久"><DateTimeInput value={s.expire_at} onChange={(v) => set("expire_at", v)} placeholder="永久有效" /></Field>
              <Field label="流量限额 (GiB)" hint="软限制：仅在 Userinfo 中体现，用尽后订阅返回空"><Input type="number" min={0} step="0.1" value={s.traffic_limit_gb} onChange={(e) => set("traffic_limit_gb", e.target.value)} placeholder="0 为不限" /></Field>
              <Field label="重置日" hint="1–28 固定那天；29/30/31 都是每月最后一天。不选则不重置。"><ResetDayInput value={s.reset_day} onChange={(v) => set("reset_day", v)} /></Field>
              <div className="flex flex-col gap-3 sm:col-span-2">
                <Switch checked={s.userinfo_header} onChange={(v) => set("userinfo_header", v)} label="输出 Subscription-Userinfo 头（客户端显示流量/到期）" />
                <Switch checked={s.show_info_nodes} onChange={(v) => set("show_info_nodes", v)} label="在节点列表顶部插入信息节点（剩余流量 / 到期 / 各外部订阅用量）" />
                <Switch checked={s.short_link} onChange={(v) => set("short_link", v)} label="同时生成 /r 别名（同样 256 位）" />
                <Switch checked={s.enabled} onChange={(v) => set("enabled", v)} label="启用" />
              </div>
              <Field label="授权用户" hint="普通用户登录后可见并复制此订阅" className="sm:col-span-2">
                <div className="flex flex-wrap gap-2">
                  {(users.data ?? []).filter((u) => u.role !== "admin").map((u) => (
                    <label key={u.id} className="inline-flex items-center gap-1.5 rounded-md border px-2 py-1 text-sm">
                      <input type="checkbox" className="accent-primary" checked={s.allowed_user_ids.includes(u.id)} onChange={(e) => set("allowed_user_ids", e.target.checked ? [...s.allowed_user_ids, u.id] : s.allowed_user_ids.filter((x) => x !== u.id))} />
                      {displayName(u)}{displayName(u) !== u.username ? ` (${u.username})` : ""}
                    </label>
                  ))}
                  {!users.data?.filter((u) => u.role !== "admin").length && <span className="text-xs text-muted-foreground">暂无普通用户，可在「用户」页添加</span>}
                </div>
              </Field>

            </div>
          </Card>
        )}

        {tab === "nodes" && (
          <div className="grid gap-4 lg:grid-cols-[1fr_20rem]">
            <Card className="p-4">
              <div className="mb-3 flex flex-wrap items-center gap-3">
                <Switch checked={!!s.node_selection.include_all} onChange={(v) => setSel({ include_all: v })} label="包含全部节点（新增节点自动加入）" />
                <span className="text-xs text-muted-foreground">已选 {selectedNodes.length} 个</span>
              </div>
              {!s.node_selection.include_all && (
                <>
                  <p className="mb-2 text-xs text-muted-foreground">按订阅源整体勾选（同步后新节点自动包含）：</p>
                  <div className="mb-3 flex flex-wrap gap-2">
                    {(externals.data ?? []).map((e) => (
                      <label key={e.id} className="inline-flex items-center gap-1.5 rounded-md border px-2 py-1 text-sm">
                        <input type="checkbox" className="accent-primary" checked={s.node_selection.external_sub_ids.includes(e.id)} onChange={(ev) => setSel({ external_sub_ids: ev.target.checked ? [...s.node_selection.external_sub_ids, e.id] : s.node_selection.external_sub_ids.filter((x) => x !== e.id) })} />
                        {e.name} <span className="text-xs text-muted-foreground">({e.node_count})</span>
                      </label>
                    ))}
                    {allTags.map((t) => (
                      <label key={t} className="inline-flex items-center gap-1.5 rounded-md border border-dashed px-2 py-1 text-sm">
                        <input type="checkbox" className="accent-primary" checked={!!s.node_selection.tags?.includes(t)} onChange={(ev) => setSel({ tags: ev.target.checked ? [...(s.node_selection.tags ?? []), t] : (s.node_selection.tags ?? []).filter((x) => x !== t) })} />
                        标签 {t}
                      </label>
                    ))}
                  </div>
                  <NodePicker nodes={allNodes} selected={s.node_selection.node_ids} onChange={(ids) => setSel({ node_ids: ids })} />
                </>
              )}
            </Card>
            <div className="space-y-4">
              <Card className="p-4">
                <p className="mb-2 text-sm font-medium">名称过滤</p>
                <div className="space-y-3">
                  <Field label="仅包含（正则）"><Input className="mono" value={s.node_selection.filter ?? ""} onChange={(e) => setSel({ filter: e.target.value })} placeholder="HK|JP|SG" /></Field>
                  <Field label="排除（正则）"><Input className="mono" value={s.node_selection.exclude_filter ?? ""} onChange={(e) => setSel({ exclude_filter: e.target.value })} placeholder="过期|剩余|官网" /></Field>
                </div>
              </Card>
              <Card className="p-4">
                <div className="mb-2 flex items-center justify-between"><p className="text-sm font-medium">链式代理</p><Button size="sm" variant="outline" onClick={() => set("chains", [...s.chains, { name: "", front_node_id: 0, landing_node_id: 0 }])}><Plus className="h-4 w-4" /></Button></div>
                <p className="mb-2 text-xs text-muted-foreground">前置（入口）→ 落地（出口）。生成的链式节点可像普通节点一样放入代理组。</p>
                <div className="space-y-2">
                  {s.chains.map((c, i) => (
                    <div key={i} className="space-y-1.5 rounded-md border p-2">
                      <Input placeholder="链名，如 HK → US 落地" value={c.name} onChange={(e) => set("chains", s.chains.map((x, j) => (j === i ? { ...x, name: e.target.value } : x)))} />
                      <Select value={String(c.front_node_id)} onChange={(e) => set("chains", s.chains.map((x, j) => (j === i ? { ...x, front_node_id: Number(e.target.value) } : x)))}>
                        <option value="0">前置节点…</option>{allNodes.map((n) => <option key={n.id} value={n.id}>{n.name}</option>)}
                      </Select>
                      <Select value={String(c.landing_node_id)} onChange={(e) => set("chains", s.chains.map((x, j) => (j === i ? { ...x, landing_node_id: Number(e.target.value) } : x)))}>
                        <option value="0">落地节点…</option>{allNodes.map((n) => <option key={n.id} value={n.id}>{n.name}</option>)}
                      </Select>
                      <div className="text-right"><Button size="sm" variant="ghost" className="text-red-500" onClick={() => set("chains", s.chains.filter((_, j) => j !== i))}><Trash2 className="h-4 w-4" /></Button></div>
                    </div>
                  ))}
                  {s.chains.length === 0 && <p className="text-xs text-muted-foreground">无</p>}
                </div>
              </Card>
            </div>
          </div>
        )}

        {tab === "groups" && (
          <Card className="p-4">
            <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
              <p className="text-sm leading-6 text-muted-foreground">代理组决定客户端怎样选择节点。已有满意的模板时，分组与规则都可留空；需要复用另一套设置时，再成套应用预设。</p>
              <div className="flex flex-wrap gap-2">
                {(s.proxy_groups.length > 0 || s.rules.length > 0) && <Button size="sm" variant="ghost" onClick={() => setRestoreTemplate(true)}>恢复模板分组与规则</Button>}
                <PresetPicker presets={presets.data ?? []} onApply={applyPreset} />
              </div>
            </div>
            <GroupEditor groups={s.proxy_groups} onChange={(g) => set("proxy_groups", g)} nodes={candidates} />
          </Card>
        )}

        {tab === "rules" && (
          <div className="grid gap-4 lg:grid-cols-[1fr_20rem]">
            <Card className="p-4">
              <div className="mb-2 flex items-center justify-between">
                <p className="text-sm font-medium">规则（{s.rules.length} 条）<span className="ml-2 font-normal text-muted-foreground">空着就用模板里的分流</span></p>
                <PresetPicker presets={presets.data ?? []} onApply={applyPreset} />
              </div>
              <Textarea className="mono" rows={18} value={s.rules.join("\n")} onChange={(e) => set("rules", e.target.value.split("\n").map((l) => l.trim()).filter(Boolean))} placeholder={"DOMAIN-SUFFIX,openai.com,节点选择\nGEOSITE,cn,DIRECT\nGEOIP,CN,DIRECT\nMATCH,节点选择"} />
              <div className="mt-2"><RulesHint /></div>
            </Card>
            <Card className="p-4">
              <p className="mb-2 text-sm font-medium">模板</p>
              <p className="mb-3 text-xs leading-5 text-muted-foreground">模板是整份底稿（DNS、组、分流）。这里只能另选一份，而且只覆盖该格式；没选的格式仍用内置默认。日常可留「按格式自动」。</p>
              <Select value={String(s.template_id)} onChange={(e) => set("template_id", Number(e.target.value))}>
                <option value="0">按格式自动（Clash / Surge / 小火箭 / sing-box 各用内置）</option>
                {(["mihomo", "surge", "shadowrocket", "singbox"] as const).map((kind) => {
                  const rows = (templates.data ?? []).filter((t) => t.kind === kind);
                  if (!rows.length) return null;
                  const label = kind === "mihomo" ? "只覆盖 Clash / FlClash" : kind === "surge" ? "只覆盖 Surge" : kind === "shadowrocket" ? "只覆盖 Shadowrocket" : "只覆盖 sing-box";
                  return (
                    <optgroup key={kind} label={label}>
                      {rows.map((t) => (
                        <option key={t.id} value={t.id}>{t.name}{t.is_builtin ? "（内置）" : ""}</option>
                      ))}
                    </optgroup>
                  );
                })}
              </Select>
              {(() => {
                const picked = (templates.data ?? []).find((t) => t.id === s.template_id);
                if (!picked) return null;
                const only = picked.kind === "mihomo" ? "Clash / FlClash" : picked.kind === "surge" ? "Surge" : picked.kind === "shadowrocket" ? "Shadowrocket" : "sing-box";
                return <p className="mt-2 text-xs text-amber-700 dark:text-amber-300">已选「{picked.name}」：只有 {only} 会用这份；其它客户端仍走内置默认。</p>;
              })()}
              <p className="mt-3 text-xs leading-5 text-muted-foreground">套用预设会一起填入「代理组」和「规则」，DNS 等全局配置仍由模板提供。规则的合并方式随客户端格式不同，请先预览。已套用的内容是副本，原预设后续修改不会自动同步。</p>
            </Card>
          </div>
        )}

        {tab === "preview" && <PreviewPane state={s} />}
      </div>
      <Confirm open={restoreTemplate} onClose={() => setRestoreTemplate(false)} title="恢复模板的分组与规则？" description="将同时清空当前订阅的自定义分组和规则，重新采用模板。节点选择不变；保存订阅后生效。" onConfirm={() => { setS((previous) => ({ ...previous, proxy_groups: [], rules: [] })); setRestoreTemplate(false); toast.success("已恢复使用模板，保存订阅后生效"); }} />
    </div>
  );
}

function safeRe(s?: string): RegExp | null {
  if (!s) return null;
  try { return new RegExp(s); } catch { return null; }
}

function NodePicker({ nodes, selected, onChange }: { nodes: Node[]; selected: number[]; onChange: (ids: number[]) => void }) {
  const [q, setQ] = React.useState("");
  const list = nodes.filter((n) => !q || n.name.toLowerCase().includes(q.toLowerCase()) || n.protocol.includes(q));
  const toggle = (id: number, v: boolean) => onChange(v ? [...selected, id] : selected.filter((x) => x !== id));
  return (
    <div>
      <div className="mb-2 flex items-center gap-2">
        <Input placeholder="搜索节点" value={q} onChange={(e) => setQ(e.target.value)} className="max-w-xs" />
        <Button size="sm" variant="ghost" onClick={() => onChange(Array.from(new Set([...selected, ...list.map((n) => n.id)])))}>全选可见</Button>
        <Button size="sm" variant="ghost" onClick={() => onChange(selected.filter((id) => !list.some((n) => n.id === id)))}>取消可见</Button>
      </div>
      <div className="max-h-96 overflow-auto rounded-md border">
        {list.map((n) => (
          <label key={n.id} className="flex cursor-pointer items-center gap-3 border-b px-3 py-2 text-sm last:border-0 hover:bg-muted/40">
            <input type="checkbox" className="accent-primary" checked={selected.includes(n.id)} onChange={(e) => toggle(n.id, e.target.checked)} />
            <span className="min-w-0 flex-1 break-words">{n.name}</span>
            <Badge variant="outline">{PROTOCOL_LABELS[n.protocol] ?? n.protocol}</Badge>
            <span className="hidden text-xs text-muted-foreground sm:inline">{n.source === "deployed" ? n.server_name : n.source === "imported" ? n.external_name : "手动"}</span>
          </label>
        ))}
        {list.length === 0 && <p className="p-3 text-sm text-muted-foreground">无节点</p>}
      </div>
    </div>
  );
}

function PresetPicker({ presets, onApply }: { presets: Preset[]; onApply: (p: Preset) => void }) {
  const [open, setOpen] = React.useState(false);
  const [pick, setPick] = React.useState<Preset | null>(null);
  return (
    <>
      <Button size="sm" variant="outline" onClick={() => { setPick(null); setOpen(true); }}><Wand2 className="h-4 w-4" /> 套用分组与规则</Button>
      <Dialog open={open} onClose={() => setOpen(false)} title="套用分组与规则" description="成套替换当前订阅的自定义分组与规则，避免规则引用不存在的组。节点选择、DNS 和其他模板设置不变；保存订阅后生效。"
        footer={<><Button variant="outline" onClick={() => setOpen(false)}>取消</Button><Button disabled={!pick} onClick={() => { if (pick) onApply(pick); setOpen(false); }}>替换分组与规则</Button></>}>
        <div className="space-y-2">
          {presets.map((p) => (
            <button key={p.id} aria-pressed={pick?.id === p.id} onClick={() => setPick(p)} className={cn("w-full rounded-md border p-3 text-left text-sm transition-colors hover:bg-accent/40", pick?.id === p.id && "border-primary bg-primary/5")}>
              <p className="font-medium">{p.name} {p.is_builtin && <Badge variant="secondary" className="ml-1">内置</Badge>}</p>
              <p className="mt-1 text-xs text-muted-foreground">{(p.groups ?? []).length} 个代理组：{(p.groups ?? []).map((g) => g.name).join("、")} · {(p.rules ?? []).length} 条规则</p>
            </button>
          ))}
          {presets.length === 0 && <p className="text-sm text-muted-foreground">暂无预设，可在「模板 → 分组与规则预设」创建；也可以直接使用模板默认设置。</p>}
        </div>
        <p className="mt-3 text-xs leading-5 text-muted-foreground">这是一次复制。之后编辑原预设不会自动更新这个订阅；需要重新套用。应用后请预览目标客户端格式。</p>
      </Dialog>
    </>
  );
}

function PreviewPane({ state }: { state: EditorState }) {
  const { meta } = useAuth();
  const [format, setFormat] = React.useState(state.default_format || "mihomo");
  const m = useMutation({ mutationFn: () => post<{ format: string; body: string; node_names: string[]; groups: ProxyGroup[] }>("/api/v1/subscriptions/preview", { ...toPayload(state), format }) });
  React.useEffect(() => { m.mutate(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [format]);
  return (
    <div className="grid gap-4 lg:grid-cols-[1fr_18rem]">
      <Card className="p-4">
        <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
          <Tabs value={format} onChange={setFormat} items={(meta?.formats ?? ["mihomo", "surge", "singbox", "raw"]).map((f) => ({ value: f, label: FORMAT_LABELS[f] ?? f }))} />
          <Button size="sm" variant="outline" onClick={() => m.mutate()} loading={m.isPending}><RefreshCw className="h-4 w-4" /> 刷新</Button>
        </div>
        {m.isPending && !m.data ? <Spinner /> : m.error ? <p className="text-sm text-red-500">{(m.error as Error).message}</p> : <Pre className="max-h-[65vh] whitespace-pre-wrap break-all">{m.data?.body}</Pre>}
      </Card>
      <Card className="p-4">
        <p className="mb-2 text-sm font-medium">将输出 {m.data?.node_names.length ?? 0} 个节点</p>
        <ul className="max-h-64 space-y-0.5 overflow-auto text-xs text-muted-foreground">{m.data?.node_names.map((n) => <li key={n} className="break-words">{n}</li>)}</ul>
        {m.data?.groups?.length ? (
          <>
            <p className="mb-2 mt-4 text-sm font-medium">解析后的代理组</p>
            <ul className="space-y-1 text-xs">{m.data.groups.map((g) => <li key={g.name}><span className="font-medium">{g.name}</span> <span className="text-muted-foreground">({g.type}) → {(g.proxies ?? []).length} 项</span></li>)}</ul>
          </>
        ) : null}
        <p className="mt-4 text-xs text-muted-foreground">预览使用当前未保存的编辑内容；<Code>Subscription-Userinfo</Code> 头以实际访问时为准。</p>
      </Card>
    </div>
  );
}
