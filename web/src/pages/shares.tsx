import * as React from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, Navigate, useNavigate, useParams } from "react-router-dom";
import { Pause, Play, Plus, RotateCcw, Trash2, Pencil, Ban, KeyRound, ScrollText } from "lucide-react";
import { del, get, post, put } from "@/lib/api";
import type { Share, ShareStatus, Server, Node, RuleTemplate, User, Series, ShareTarget, ShareEntry } from "@/lib/types";
import { cn, displayName, fmtBytes, fmtDate, fmtAgo, gbToBytes, bytesToGb, parseResetDay, STATUS_LABELS, PROTOCOL_LABELS } from "@/lib/utils";
import { Badge, Button, Card, CardContent, CardHeader, CardTitle, Confirm, Dialog, Empty, Field, Input, PageHeader, Progress, Select, Spinner, Switch, Table, Th, Textarea } from "@/components/ui";
import { DateTimeInput, ResetDayInput } from "@/components/datetime-picker";
import { useToast } from "@/components/toast";
import { useAuth, useIsAdmin } from "@/lib/auth";
import { TrafficBars } from "@/components/charts";
import { TrafficIO } from "@/components/traffic-ways";
import { BILLING_OPTIONS, billingLabel, billingMode, trafficUsage, trafficPeriodDate, validGiB, type BillingMode } from "@/lib/traffic-billing";
import { SubscriptionLinks } from "@/pages/subscriptions";
import { NodeRow, NodeDetailDialog } from "@/pages/nodes";
import { PersonalTemplates } from "@/components/personal-templates";

export function StatusBadge({ s }: { s: ShareStatus }) {
  const v = s === "active" ? "success" : s === "paused" ? "warning" : s === "exhausted" ? "destructive" : "secondary";
  return <Badge variant={v}>{STATUS_LABELS[s] ?? s}</Badge>;
}

// ShareCard is used on the dashboard (user view) and in the list.
export function ShareCard({ share, link = true }: { share: Share; link?: boolean }) {
  const u = share.usage;
  const usage = trafficUsage(u, share.billing_mode, { inbound: share.used_upload, outbound: share.used_download, quota: share.quota_bytes });
  const body = (
    <Card className="h-full p-4 transition-colors hover:bg-accent/30">
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <p className="wrap-break-word font-medium">{share.name}</p>
          <p className="wrap-break-word text-xs text-muted-foreground">{share.user_name ? `用户 ${share.user_name} · ` : ""}{share.targets.length} 台服务器 · {share.targets.reduce((a, t) => a + entryCountOf(t), 0) + share.extra_node_ids.length} 节点</p>
        </div>
        <StatusBadge s={share.status} />
      </div>
      <div className="mt-3">
        <div className="mb-1 flex justify-between text-xs text-muted-foreground">
          <span>计入配额已用（{billingLabel(usage.mode)}）</span>
          <span>{usage.quota > 0 ? `${usage.percent.toFixed(0)}%` : "不限量"}</span>
        </div>
        <p className="mb-1.5 text-sm font-medium tabular-nums">{fmtBytes(usage.billed)}{usage.quota > 0 && ` / ${fmtBytes(usage.quota)} · 剩余 ${fmtBytes(usage.remaining)}`}</p>
        <Progress value={usage.percent} />
        <TrafficIO className="mt-1.5" compact inbound={usage.inbound} outbound={usage.outbound} />
        <p className="mt-1 text-xs text-muted-foreground">
          {u.next_reset ? `下次重置 ${trafficPeriodDate(u.next_reset)}（UTC）` : "不重置"}
          {share.expires_at && ` · 到期 ${fmtDate(share.expires_at, false)}`}
        </p>
      </div>
    </Card>
  );
  return link ? <Link to={`/shares/${share.public_id}`} className="block min-w-0">{body}</Link> : body;
}

export function SharesPage() {
  const admin = useIsAdmin();
  const q = useQuery({ queryKey: ["shares"], queryFn: () => get<Share[]>("/api/v1/shares"), refetchInterval: 30000 });
  const [create, setCreate] = React.useState(false);
  const [filter, setFilter] = React.useState<"" | ShareStatus>("");
  const list = (q.data ?? []).filter((s) => !filter || s.status === filter);
  if (!admin && q.data?.length) return <Navigate to={`/shares/${q.data[0].public_id}`} replace />;
  return (
    <div>
      <PageHeader title="分享" description={admin ? "独立入口、独立配额、独立订阅链接" : undefined} actions={admin && <Button onClick={() => setCreate(true)}><Plus className="h-4 w-4" /> 新建分享</Button>} />
      <div className="mb-4 flex flex-wrap gap-1.5">
        {(["", "active", "exhausted", "paused", "expired", "revoked"] as const).map((s) => (
          <Badge key={s} variant={filter === s ? "default" : "outline"} className="cursor-pointer" onClick={() => setFilter(s)}>{s === "" ? "全部" : STATUS_LABELS[s]} {s === "" ? q.data?.length ?? 0 : q.data?.filter((x) => x.status === s).length ?? 0}</Badge>
        ))}
      </div>
      {q.isLoading ? <Spinner /> : !list.length ? (
        <Empty title="没有分享" description={admin ? "创建分享后会在所选服务器上自动部署专属入口，流量独立计量并可设配额。" : "管理员尚未给你分配。"} action={admin && <Button onClick={() => setCreate(true)}>新建分享</Button>} />
      ) : (
        <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">{list.map((s) => <ShareCard key={s.id} share={s} />)}</div>
      )}
      <ShareDialog open={create} onClose={() => setCreate(false)} />
    </div>
  );
}

// ---------- create / edit ----------
interface Form {
  name: string;
  user_id: number;
  targets: ShareTarget[];
  extra_node_ids: number[];
  quota_gb: string;
  billing_mode: BillingMode;
  reset_day: string;
  expires_at: string;
  template_id: number;
  connlog_enabled: boolean;
  notes: string;
}
const emptyForm: Form = { name: "", user_id: 0, targets: [], extra_node_ids: [], quota_gb: "100", billing_mode: "dual", reset_day: "1", expires_at: "", template_id: 0, connlog_enabled: false, notes: "" };

function entryCountOf(t: ShareTarget) { return (t.protocols?.length ?? 0) + (t.entries?.length ?? 0); }
function entryLabel(e: ShareEntry) {
  const c = e.snapshot;
  const combo = c.vless ?? c.trojan;
  if (combo) return `${c.vless ? "VLESS" : "Trojan"} · ${combo.transport === "grpc" ? "gRPC" : combo.transport.toUpperCase()} + ${(combo.security ?? (c.vless ? "reality" : "tls")).toUpperCase()}`;
  if (c.protocol === "trojan") return "Trojan · TCP + TLS";
  if (c.protocol === "ss") return `SS · ${c.ss_network === "both" ? "TCP + UDP" : "TCP"}`;
  if (c.mieru_transport) return `Mieru · ${c.mieru_transport}`;
  return PROTOCOL_LABELS[c.protocol] ?? c.protocol;
}
function entryOptions(protocols: string[], saved: ShareEntry[]): ShareEntry[] {
  const options = new Map<string, ShareEntry>();
  const add = (id: string, snapshot: ShareEntry["snapshot"]) => options.set(id, { id, snapshot, quota_bytes: 0 });
  for (const protocol of protocols) {
    if (protocol !== "vless" && protocol !== "trojan") continue;
    if (protocol === "vless") {
      add("vless-tcp", { protocol, vless: { transport: "tcp", security: "reality" } });
      add("vless-xhttp", { protocol, vless: { transport: "xhttp", security: "reality", xhttp: { path: "/", mode: "auto" } } });
      add("vless-tcp-tls", { protocol, vless: { transport: "tcp", security: "tls" } });
    } else {
      add("trojan", { protocol }); // Keep the original TCP/TLS entry ID and snapshot.
    }
    add(`${protocol}-ws-tls`, { protocol, [protocol]: { transport: "ws", security: "tls", ws: { path: "/" } } });
    add(`${protocol}-grpc-tls`, { protocol, [protocol]: { transport: "grpc", security: "tls", grpc: { service_name: "grpc" } } });
  }
  for (const e of saved) options.set(e.id, e);
  return [...options.values()];
}

function managedTLS({ vless, trojan }: ShareEntry["snapshot"]) {
  return vless?.security === "tls" || trojan?.transport === "ws" || trojan?.transport === "grpc";
}

function entryBlockReason(e: ShareEntry, s: Server, network?: ShareTarget["network"]) {
  if (e.snapshot.vless?.transport === "xhttp") return s.xhttp_block_reason ?? "未确认 XHTTP 主机能力";
  if (managedTLS(e.snapshot)) {
    if (network && !e.node_id) return "仅支持默认网络";
    return s.tls_transport_block_reason ?? "未确认 TLS 传输部署能力";
  }
  return "";
}

function TLSTransportFields({ entry, server, onChange }: { entry: ShareEntry; server: Server; onChange: (e: ShareEntry) => void }) {
  const key = entry.snapshot.protocol === "trojan" ? "trojan" : "vless";
  const combo = entry.snapshot[key];
  if (!combo || !managedTLS(entry.snapshot)) return null;
  const patch = (delta: Partial<ShareEntry["snapshot"]>) => onChange({ ...entry, snapshot: { ...entry.snapshot, ...delta } });
  const update = (delta: Partial<typeof combo>) => patch({ [key]: { ...combo, ...delta } });
  const label = entryLabel(entry);
  const frozen = !!entry.node_id;
  const mode = entry.snapshot.cert_mode || server.cert_mode;
  return <div className="grid gap-2">
    {combo.transport === "ws" && <>
      <Field label="WS 路径"><Input aria-label={`${label} WS 路径`} maxLength={512} value={combo.ws?.path ?? "/"} disabled={frozen} onChange={ev => update({ ws: { ...combo.ws, path: ev.target.value } })} /></Field>
      <Field label="WS Host（可选）"><Input aria-label={`${label} WS Host`} value={combo.ws?.host ?? ""} disabled={frozen} onChange={ev => update({ ws: { ...combo.ws, host: ev.target.value } })} /></Field>
    </>}
    {combo.transport === "grpc" && <Field label="gRPC 服务名"><Input aria-label={`${label} gRPC 服务名`} maxLength={128} value={combo.grpc?.service_name ?? ""} disabled={frozen} onChange={ev => update({ grpc: { service_name: ev.target.value } })} /></Field>}
    <Field label="TLS 域名（SNI）" hint="留空使用服务器访问地址，与 WS Host 分开设置。"><Input aria-label={`${label} TLS 域名`} value={entry.snapshot.domain ?? ""} disabled={frozen} onChange={ev => patch({ domain: ev.target.value || undefined })} /></Field>
    <Field label="证书模式"><Select aria-label={`${label} 证书模式`} disabled={frozen} value={entry.snapshot.cert_mode ?? ""} onChange={ev => patch({ cert_mode: ev.target.value || undefined, cert_id: (ev.target.value || server.cert_mode) === "external" ? entry.snapshot.cert_id : undefined })}>
      <option value="">继承服务器（{server.cert_mode || "self_signed"}）</option><option value="self_signed">自签证书</option><option value="acme">ACME</option><option value="external">外部证书</option>
    </Select></Field>
    {mode === "external" && <Field label="外部证书 ID" hint="仅填 VPS 本机安全策略中登记的 ID，不要填写密钥或文件路径。"><Input aria-label={`${label} 外部证书 ID`} maxLength={64} value={entry.snapshot.cert_id ?? ""} disabled={frozen} onChange={ev => patch({ cert_id: ev.target.value || undefined })} /></Field>}
  </div>;
}

function toLocal(iso: string) {
  const d = new Date(iso);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

export function ShareDialog({ open, onClose, share }: { open: boolean; onClose: () => void; share?: Share }) {
  const qc = useQueryClient();
  const toast = useToast();
  const nav = useNavigate();
  const { meta } = useAuth();
  const servers = useQuery({ queryKey: ["servers"], queryFn: () => get<Server[]>("/api/v1/servers"), enabled: open });
  const nodes = useQuery({ queryKey: ["nodes", { source: "" }], queryFn: () => get<Node[]>("/api/v1/nodes"), enabled: open });
  const users = useQuery({ queryKey: ["users"], queryFn: () => get<User[]>("/api/v1/users"), enabled: open });
  const templates = useQuery({ queryKey: ["templates"], queryFn: () => get<RuleTemplate[]>("/api/v1/templates"), enabled: open });
  const [f, setF] = React.useState<Form>(emptyForm);
  const [nodeQuotas, setNodeQuotas] = React.useState<Record<string, string>>({});
  React.useEffect(() => {
    setF(share ? { name: share.name, user_id: share.user_id ?? 0, targets: (share.targets ?? []).map(t => ({ ...t, protocols: t.protocols ?? [] })), extra_node_ids: share.extra_node_ids ?? [], quota_gb: bytesToGb(share.quota_bytes), billing_mode: billingMode(share.billing_mode), reset_day: String(share.reset_day ?? 0), expires_at: share.expires_at ? toLocal(share.expires_at) : "", template_id: share.template_id ?? 0, connlog_enabled: share.connlog_enabled, notes: share.notes } : emptyForm);
    setNodeQuotas(Object.fromEntries((share?.targets ?? []).flatMap(t => [...(t.protocols ?? []).map(p => [`${t.server_id}:${p}`, bytesToGb(t.quota_bytes?.[p] ?? 0)]), ...(t.entries ?? []).map(e => [`${t.server_id}:entry:${e.id}`, bytesToGb(e.quota_bytes)])])));
  }, [share, open]);
  const set = <K extends keyof Form>(k: K, v: Form[K]) => setF((p) => ({ ...p, [k]: v }));
  const m = useMutation({
    mutationFn: () => {
      const targets = f.targets.map(t => ({ ...t, entries: t.entries?.map(e => ({ ...e, quota_bytes: gbToBytes(nodeQuotas[`${t.server_id}:entry:${e.id}`] ?? "") })), quota_bytes: Object.fromEntries(t.protocols.map(p => [p, gbToBytes(nodeQuotas[`${t.server_id}:${p}`] ?? "")])) }));
      const payload = { ...f, entry_version: 1, targets, user_id: f.user_id || null, quota_bytes: gbToBytes(f.quota_gb), reset_day: parseResetDay(f.reset_day), template_id: f.template_id || null, expires_at: f.expires_at ? new Date(f.expires_at).toISOString() : "0001-01-01T00:00:00Z" };
      return share ? put<Share>(`/api/v1/shares/${share.id}`, payload) : post<Share>("/api/v1/shares", payload);
    },
    onSuccess: (r) => {
      qc.invalidateQueries({ queryKey: ["shares"] }); qc.invalidateQueries({ queryKey: ["nodes"] }); qc.invalidateQueries({ queryKey: ["subscriptions"] });
      qc.invalidateQueries({ queryKey: ["dashboard"] });
      toast.success(share ? "已保存，节点变更将在下次心跳生效" : "分享已创建");
      onClose();
      if (!share) nav(`/shares/${r.public_id}`);
    },
    onError: (e) => toast.fromError(e),
  });
  const toggleProto = (serverID: number, proto: string, on: boolean) => {
    const t = f.targets.find((x) => x.server_id === serverID);
    let next: ShareTarget[];
    if (!t) next = on ? [...f.targets, { server_id: serverID, protocols: [proto] }] : f.targets;
    else {
      const protocols = on ? Array.from(new Set([...t.protocols, proto])) : t.protocols.filter((p) => p !== proto);
      next = protocols.length || t.entries?.length ? f.targets.map((x) => (x.server_id === serverID ? { ...x, protocols } : x)) : f.targets.filter((x) => x.server_id !== serverID);
    }
    set("targets", next);
  };
  const setEntries = (serverID: number, entries: ShareEntry[]) => {
    const target = f.targets.find(t => t.server_id === serverID) ?? { server_id: serverID, protocols: [] };
    set("targets", [...f.targets.filter(t => t.server_id !== serverID), ...(entries.length || target.protocols.length ? [{ ...target, entries }] : [])]);
  };
  const extraCandidates = (nodes.data ?? []).filter((n) => !n.share_id && !n.revoked && n.source !== "deployed");
  const enabledServers = (servers.data ?? []).filter((s) => s.enabled);
  const entryCount = f.targets.reduce((n, t) => n + entryCountOf(t), 0);
  // Single column sized to the content: the old two-column layout left the
  // right half empty whenever there were no servers / spare nodes.
  return (
    <Dialog open={open} onClose={onClose} title={share ? "编辑分享" : "新建分享"} size="md" description="每个勾选的「服务器 × 组合」会生成一个独立端口与凭据的专属入口，专属节点独立计量。"
      footer={<><Button variant="outline" onClick={onClose}>取消</Button><Button onClick={() => m.mutate()} loading={m.isPending} disabled={!validGiB(f.quota_gb, true) || f.targets.some(t => [...t.protocols, ...(t.entries ?? []).map(e => `entry:${e.id}`)].some(p => !validGiB(nodeQuotas[`${t.server_id}:${p}`] ?? "", true)))}>{share ? "保存" : "创建"}</Button></>}>
      <div className="space-y-5">
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="名称"><Input value={f.name} onChange={(e) => set("name", e.target.value)} placeholder="给小王" /></Field>
          <Field label="绑定用户" hint="可选；该用户登录后可看到用量与订阅">
            <Select value={String(f.user_id)} onChange={(e) => set("user_id", Number(e.target.value))}>
              <option value="0">不绑定</option>
              {(users.data ?? []).filter((u) => u.role !== "admin").map((u) => <option key={u.id} value={u.id}>{displayName(u)}{displayName(u) !== u.username ? ` (${u.username})` : ""}</option>)}
            </Select>
          </Field>
        </div>

        <section>
          <div className="mb-2 flex items-center justify-between">
            <p className="text-sm font-medium">专属入口</p>
            {entryCount > 0 && <Badge variant="info">{entryCount} 个入口</Badge>}
          </div>
          {servers.isLoading ? <Spinner /> : enabledServers.length === 0 ? (
            <div className="flex flex-wrap items-center justify-between gap-2 rounded-xl border border-dashed px-4 py-3 text-sm text-muted-foreground">
              <span>还没有服务器，添加后才能生成专属入口。</span>
              <Button size="sm" variant="outline" onClick={() => { onClose(); nav("/servers"); }}>去添加服务器</Button>
            </div>
          ) : (
            <div className={cn("grid gap-2", enabledServers.length > 1 && "sm:grid-cols-2")}>
              {enabledServers.map((s) => {
                const t = f.targets.find((x) => x.server_id === s.id);
                const protos = (meta?.protocols ?? []).filter((p) => !(s.core_mode === "lean" && ["snell","mieru"].includes(p)));
                const saved = share?.targets.find(x => x.server_id === s.id);
                const plainProtocols = new Set([...protos.filter(p => p !== "vless" && p !== "trojan"), ...(saved?.protocols ?? [])]);
                return (
                  <div key={s.id} className="rounded-xl border p-3">
                    <div className="mb-2 flex items-center justify-between gap-2">
                      <p className="min-w-0 wrap-break-word text-sm font-medium">{s.name} <span className="text-xs text-muted-foreground">{s.region}{s.region ? " · " : ""}{s.agent_status === "online" ? "在线" : STATUS_LABELS[s.agent_status]}</span></p>
                      {t && <Badge variant="info">{entryCountOf(t)}</Badge>}
                    </div>
                    <div className="flex flex-wrap gap-1.5">
                      {[...plainProtocols].map((p) => (
                        <label key={p} className={cn("inline-flex cursor-pointer items-center gap-1 rounded-md border px-2 py-1 text-xs", t?.protocols.includes(p) && "border-primary/40 bg-primary/10 text-primary")}>
                          <input type="checkbox" className="accent-primary" checked={!!t?.protocols.includes(p)} onChange={(e) => toggleProto(s.id, p, e.target.checked)} />
                          {PROTOCOL_LABELS[p] ?? p}{(p === "vless" || p === "trojan") && "（原入口）"}
                        </label>
                      ))}
                      {entryOptions(protos, saved?.entries ?? []).map(option => {
                        const selected = t?.entries?.find(e => e.id === option.id);
                        const reason = entryBlockReason(option, s, t?.network);
                        return <label key={`entry:${option.id}`} title={reason || option.id} className={cn("inline-flex items-center gap-1 rounded-md border px-2 py-1 text-xs", selected && "border-primary/40 bg-primary/10 text-primary", !selected && reason && "text-muted-foreground")}>
                          <input type="checkbox" className="accent-primary" checked={!!selected} disabled={!selected && !!reason} onChange={event => setEntries(s.id, event.target.checked ? [...(t?.entries ?? []), option] : (t?.entries ?? []).filter(e => e.id !== option.id))} />
                          {entryLabel(option)}{reason && <span>（{reason}）</span>}
                        </label>;
                      })}
                    </div>
                    {t && <div className="mt-3 space-y-2">{t.protocols.map(p => <Field key={p} label={`${PROTOCOL_LABELS[p] ?? p} 额度 (GiB)`}>
                      <Input aria-label={`${s.name} ${PROTOCOL_LABELS[p] ?? p} 节点额度`} type="number" min={0} step="any" placeholder="不限" value={nodeQuotas[`${s.id}:${p}`] ?? ""} onChange={e => setNodeQuotas(prev => ({ ...prev, [`${s.id}:${p}`]: e.target.value }))} />
                    </Field>)}</div>}
                    {t?.entries?.map(e => <div key={e.id} className="mt-3 space-y-2">
                      <Field label={`${entryLabel(e)} 额度 (GiB)`} hint={`入口 ID：${e.id}`}><Input aria-label={`${s.name} ${e.id} 节点额度`} type="number" min={0} step="any" placeholder="不限" value={nodeQuotas[`${s.id}:entry:${e.id}`] ?? ""} onChange={ev => setNodeQuotas(prev => ({ ...prev, [`${s.id}:entry:${e.id}`]: ev.target.value }))} /></Field>
                      <TLSTransportFields entry={e} server={s} onChange={next => setEntries(s.id, (t.entries ?? []).map(it => it.id === e.id ? next : it))} />
                      {e.snapshot.vless?.transport === "xhttp" && (() => {
                        const x = e.snapshot.vless.xhttp ?? {};
                        const update = (field: "path" | "host" | "mode", value: string) => setEntries(s.id, (t.entries ?? []).map(it => it.id === e.id ? { ...it, snapshot: { ...it.snapshot, vless: { ...e.snapshot.vless!, xhttp: { ...x, [field]: value } } } } : it));
                        return <div className="grid gap-2"><Field label="XHTTP 路径"><Input aria-label="XHTTP 路径" value={x.path ?? "/"} disabled={!!e.node_id} onChange={ev => update("path", ev.target.value)} /></Field><Field label="HTTP Host（可选）"><Input aria-label="HTTP Host" value={x.host ?? ""} disabled={!!e.node_id} onChange={ev => update("host", ev.target.value)} /></Field><Field label="XHTTP 模式"><Select aria-label="XHTTP 模式" value={x.mode ?? "auto"} disabled={!!e.node_id} onChange={ev => update("mode", ev.target.value)}>{["auto", "packet-up", "stream-up", "stream-one"].map(v => <option key={v}>{v}</option>)}</Select></Field></div>;
                      })()}
                    </div>)}
                    {t && <Field label="新入口网络" hint="仅在新建入口时复制监听与固定出口版本，已有入口保持各自设置。应用会重启共享代理。"><Select aria-label={`分享网络 ${s.name}`} disabled={!t.network && !!t.entries?.some(e => !e.node_id && managedTLS(e.snapshot))} value={t.network?JSON.stringify(t.network):""} onChange={e=>set("targets",f.targets.map(x=>x.server_id===s.id?{...x,network:e.target.value?JSON.parse(e.target.value):undefined}:x))}>
                    <option value="">默认网络</option>
                    {t.network&&<option value={JSON.stringify(t.network)}>已保存的网络模板</option>}
                    {(nodes.data??[]).filter(n=>n.server_id===s.id&&n.network&&n.network.advertise_mode==="inherit"&&!n.revoked).map(n=><option key={n.id} value={JSON.stringify(n.network)}>复制 {n.name} 的网络 · v{n.network_revision}</option>)}
                    </Select></Field>}
                  </div>
                );
              })}
            </div>
          )}
        </section>

        <p className="text-xs text-muted-foreground">节点额度留空或 0 为不限；单个节点用尽只停该入口，分享总额度同时生效。节点沿用分享的计费方式与重置日。已有分享的节点用量从支持此功能的版本升级后开始累计。</p>
        {extraCandidates.length > 0 && (
          <section>
            <p className="mb-1 text-sm font-medium">附加节点</p>
            <p className="mb-2 text-xs text-muted-foreground">附加节点复用原有凭据，无法将用量归属这份分享，不计入分享的已采集流量与配额，也无法按这份分享硬性限流，建议只作备用。</p>
            <div className="max-h-44 overflow-auto rounded-xl border">
              {extraCandidates.map((n) => (
                <label key={n.id} className="flex items-center gap-2 border-b px-3 py-1.5 text-sm last:border-0">
                  <input type="checkbox" className="accent-primary" checked={f.extra_node_ids.includes(n.id)} onChange={(e) => set("extra_node_ids", e.target.checked ? [...f.extra_node_ids, n.id] : f.extra_node_ids.filter((x) => x !== n.id))} />
                  <span className="min-w-0 flex-1 wrap-break-word">{n.name}</span><span className="text-xs text-muted-foreground">{PROTOCOL_LABELS[n.protocol] ?? n.protocol}</span>
                </label>
              ))}
            </div>
          </section>
        )}

        <section className="space-y-3">
          <p className="text-sm font-medium">配额与有效期</p>
          <div className="grid grid-cols-2 gap-3">
            <Field label="分享总额度 (GiB)" hint="0 为不限，所有专属节点共同计入"><Input type="number" min={0} step="any" value={f.quota_gb} onChange={(e) => set("quota_gb", e.target.value)} /></Field>
            <Field label="重置日" hint="1–28 固定那天；29/30/31 都是每月最后一天。"><ResetDayInput value={f.reset_day} onChange={(v) => set("reset_day", v)} /></Field>
          </div>
          <Field label="流量计费方式" hint="这份分享独立计费，不继承服务器方式。较大值按本期入站、出站分别累计后比较；流量图仍展示原始方向。">
            <Select aria-label="分享流量计费方式" value={f.billing_mode} onChange={(e) => set("billing_mode", billingMode(e.target.value))}>
              {BILLING_OPTIONS.map(({ value, label }) => <option key={value} value={value}>{label}</option>)}
            </Select>
          </Field>
          {share && billingMode(share.billing_mode) !== f.billing_mode && <p className="text-xs text-muted-foreground">保存后按新方式重新计算本期已采集流量，不会清零用量；达到配额时可能停用分享。</p>}
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="到期时间" hint="留空永久"><DateTimeInput value={f.expires_at} onChange={(v) => set("expires_at", v)} placeholder="永久有效" /></Field>
            <Field label="订阅模板" hint="留空用内置默认（地区组 / 应用组 / 分流规则）">
              <Select value={String(f.template_id)} onChange={(e) => set("template_id", Number(e.target.value))}>
                <option value="0">默认</option>
                {(templates.data ?? []).map((t) => <option key={t.id} value={t.id}>[{t.kind}] {t.name}</option>)}
              </Select>
            </Field>
          </div>
        </section>

        <Switch checked={f.connlog_enabled} onChange={(v) => set("connlog_enabled", v)} disabled={!meta?.connlog} label={`记录该分享的连接日志（目标 / 客户端）${meta?.connlog ? "" : " — 服务端未启用"}`} />
        <p className="text-xs text-muted-foreground">自用订阅默认记录，见「连接日志」里的自用筛选。</p>
        <Field label="备注"><Textarea rows={2} value={f.notes} onChange={(e) => set("notes", e.target.value)} /></Field>
      </div>
    </Dialog>
  );
}

// ---------- detail ----------
export function ShareDetailPage() {
  const { id } = useParams();
  const admin = useIsAdmin();
  const { user } = useAuth();
  const nav = useNavigate();
  const qc = useQueryClient();
  const toast = useToast();
  const q = useQuery({ queryKey: ["shares", id], queryFn: () => get<Share>(`/api/v1/share-pages/${encodeURIComponent(id ?? "")}`), enabled: !!id, refetchInterval: 20000 });
  const shareID = q.data?.id;
  const traffic = useQuery({ queryKey: ["shares", shareID, "traffic"], queryFn: () => get<Series>(`/api/v1/shares/${shareID}/traffic?days=30`), enabled: !!shareID && admin });
  const events = useQuery({ queryKey: ["shares", shareID, "events"], queryFn: () => get<{ id: number; ts: string; kind: string; detail: string }[]>(`/api/v1/shares/${shareID}/events?limit=50`), enabled: !!shareID && admin });
  const [edit, setEdit] = React.useState(false);
  const [confirm, setConfirm] = React.useState<null | "revoke" | "delete" | "reset" | "reissue">(null);
  const [nodeDetail, setNodeDetail] = React.useState<Node | null>(null);
  const invalidate = () => { qc.invalidateQueries({ queryKey: ["shares"] }); qc.invalidateQueries({ queryKey: ["nodes"] }); qc.invalidateQueries({ queryKey: ["subscriptions"] }); };
  const action = useMutation({
    mutationFn: (a: string) => post<{ share: Share; token?: string }>(`/api/v1/shares/${shareID}/${a}`),
    onSuccess: (_r, a) => { toast.success({ pause: "已暂停", resume: "已恢复", reset: "用量已重置", revoke: "已撤销", reissue: "已重新签发：端口不变，凭据与订阅链接已更换" }[a] ?? "完成"); setConfirm(null); invalidate(); },
    onError: (e) => toast.fromError(e),
  });
  const delM = useMutation({ mutationFn: () => del(`/api/v1/shares/${shareID}`), onSuccess: () => { toast.success("已删除"); invalidate(); nav("/shares"); }, onError: (e) => toast.fromError(e) });
  if (q.isLoading) return <Spinner />;
  const s = q.data;
  if (!s) return <Empty title="分享不存在" />;
  const u = s.usage;
  const usage = trafficUsage(u, s.billing_mode, { inbound: s.used_upload, outbound: s.used_download, quota: s.quota_bytes });
  return (
    <div>
      <PageHeader
        back={admin ? { to: "/shares", label: "分享" } : undefined}
        title={s.name}
        description={`${s.user_name ? `用户 ${s.user_name} · ` : ""}创建于 ${fmtDate(s.created_at, false)}${s.notes ? ` · ${s.notes}` : ""}`}
        actions={
          <>
            <StatusBadge s={s.status} />
            {admin && s.status !== "revoked" && (
              <>
                {s.status === "paused" ? <Button size="sm" variant="outline" onClick={() => action.mutate("resume")} loading={action.isPending}><Play className="h-4 w-4" /> 恢复</Button>
                  : s.status !== "expired" && <Button size="sm" variant="outline" onClick={() => action.mutate("pause")} loading={action.isPending}><Pause className="h-4 w-4" /> 暂停</Button>}
                <Button size="sm" variant="outline" onClick={() => setConfirm("reset")}><RotateCcw className="h-4 w-4" /> 重置用量</Button>
                <Button size="sm" variant="outline" onClick={() => setConfirm("reissue")}><KeyRound className="h-4 w-4" /> 重新签发</Button>
                <Button size="sm" variant="outline" onClick={() => setEdit(true)}><Pencil className="h-4 w-4" /> 编辑</Button>
                <Button size="sm" variant="ghost" className="text-red-500" onClick={() => setConfirm("revoke")}><Ban className="h-4 w-4" /> 撤销</Button>
              </>
            )}
            {admin && <Button size="sm" variant="ghost" className="text-red-500" onClick={() => setConfirm("delete")}><Trash2 className="h-4 w-4" /></Button>}
          </>
        }
      />
      <div className="grid gap-4 lg:grid-cols-3">
        <Card>
          <CardHeader><CardTitle>本期用量</CardTitle></CardHeader>
          <CardContent>
            <p className="text-2xl font-semibold tabular-nums">{fmtBytes(usage.billed)}</p>
            <p className="text-xs text-muted-foreground">计入配额已用 · {usage.quota > 0 ? `${fmtBytes(usage.quota)} 配额 · ${usage.percent.toFixed(1)}% · 剩余 ${fmtBytes(usage.remaining)}` : "不限量"} · 自 {trafficPeriodDate(s.period_start)}（UTC）</p>
            <Progress className="mt-3" value={usage.percent} />
            <TrafficIO className="mt-4" inbound={usage.inbound} outbound={usage.outbound} />
            {s.extra_node_ids.length > 0 && <p className="mt-2 text-xs text-muted-foreground">以上统计仅包含这份分享的专属节点。{s.extra_node_ids.length} 个附加节点复用原有凭据，用量不计入本分享的流量或配额。</p>}
            <dl className="mt-4 space-y-1.5 text-xs text-muted-foreground">
              <div className="flex justify-between"><dt>计费方式</dt><dd>{billingLabel(usage.mode)}</dd></div>
              <div className="flex justify-between"><dt>下次重置（UTC）</dt><dd>{u.next_reset ? trafficPeriodDate(u.next_reset) : "不重置"}</dd></div>
              <div className="flex justify-between"><dt>到期</dt><dd>{s.expires_at ? fmtDate(s.expires_at) : "永久"}</dd></div>
              <div className="flex justify-between"><dt>连接日志</dt><dd>{s.connlog_enabled ? "开启" : "关闭"}</dd></div>
            </dl>
          </CardContent>
        </Card>
        <Card className="lg:col-span-2">
          <CardHeader><CardTitle>订阅链接</CardTitle></CardHeader>
          <CardContent>
            {s.subscription ? <SubscriptionLinks sub={s.subscription} /> : <p className="text-sm text-muted-foreground">尚未生成订阅</p>}
            {s.status !== "active" && <p className="mt-3 rounded-md border border-amber-500/40 bg-amber-500/5 p-2 text-xs">当前状态为「{STATUS_LABELS[s.status]}」：订阅仍可拉取，但专属入口已停止转发。</p>}
          </CardContent>
        </Card>
      </div>
      {s.user_id === user?.id && <PersonalTemplates shareID={s.id} />}
      {admin && <Card className="mt-4">
        <CardHeader><CardTitle>近 30 天流量（柱为入站/出站）</CardTitle></CardHeader>
        <CardContent>{traffic.isLoading ? <Spinner /> : traffic.isError ? <p className="text-sm text-destructive">流量加载失败，请稍后重试</p> : traffic.data?.has_data ? <><TrafficIO inbound={traffic.data.total_up} outbound={traffic.data.total_down} /><TrafficBars points={traffic.data.points} /><p className="text-xs text-muted-foreground">仅汇总这份分享已采集的节点流量；计费方式不改变原始入站、出站和汇总。</p></> : <p className="text-sm text-muted-foreground">近 30 天暂无采集记录</p>}</CardContent>
      </Card>}
      {admin && <div className="mt-4 grid gap-4 lg:grid-cols-3">
        <Card className="lg:col-span-2">
          <CardHeader><CardTitle>专属节点（{s.nodes?.length ?? 0}）</CardTitle></CardHeader>
          <CardContent>
            {!s.nodes?.length ? <p className="text-sm text-muted-foreground">无</p> : (
              <Table>
                <thead><tr className="border-b"><Th>名称</Th><Th>协议</Th><Th>地址</Th><Th>归属</Th><Th>近 30 天用量</Th><Th>状态</Th><Th></Th></tr></thead>
                <tbody>{s.nodes.map((n) => <NodeRow key={n.id} n={n} onOpen={() => setNodeDetail(n)} />)}</tbody>
              </Table>
            )}
            {!!s.nodes?.length && <div className="mt-4 space-y-2">
              <p className="text-sm font-medium">节点额度与已计量用量</p>
              <p className="text-xs text-muted-foreground">沿用分享计费方式与重置日；已有分享从支持此功能的版本升级后开始累计节点用量。</p>
              {s.nodes.map(n => {
                const target = s.targets.find(t => t.server_id === n.server_id);
                const quota = target?.entries?.find(e => e.node_id === n.id)?.quota_bytes ?? target?.quota_bytes?.[n.protocol] ?? 0;
                const raw = s.node_usage?.[String(n.id)] ?? [0, 0];
                const used = trafficUsage(undefined, s.billing_mode, { inbound: raw[0], outbound: raw[1], quota });
                return <div key={n.id} className="rounded-lg border p-3 text-sm">
                  <div className="flex flex-wrap justify-between gap-2"><span>{n.name}</span><span>{fmtBytes(used.billed)} / {quota > 0 ? fmtBytes(quota) : "不限"}{quota > 0 && used.billed >= quota && <Badge variant="warning" className="ml-2">节点额度用尽</Badge>}</span></div>
                  {quota > 0 && <Progress className="mt-2" value={used.percent} />}
                </div>;
              })}
            </div>}
            {admin && s.connlog_enabled && <Link to={`/connlog?share_id=${s.id}`} className="mt-3 inline-flex items-center gap-1 text-sm text-primary hover:underline"><ScrollText className="h-4 w-4" /> 查看该分享的连接日志</Link>}
          </CardContent>
        </Card>
        <Card>
          <CardHeader><CardTitle>事件</CardTitle></CardHeader>
          <CardContent>
            {!events.data?.length ? <p className="text-sm text-muted-foreground">无</p> : (
              <ul className="space-y-2 text-xs">
                {events.data.map((e) => <li key={e.id}><span className="text-muted-foreground">{fmtAgo(e.ts)}</span> · <span className="font-medium">{e.kind}</span>{e.detail && <span className="text-muted-foreground"> — {e.detail}</span>}</li>)}
              </ul>
            )}
          </CardContent>
        </Card>
      </div>}
      <ShareDialog open={edit} onClose={() => setEdit(false)} share={s} />
      <Confirm open={confirm === "revoke"} onClose={() => setConfirm(null)} onConfirm={() => action.mutate("revoke")} loading={action.isPending} destructive title="撤销分享？" description="专属入口从服务器上撤下、订阅链接失效。撤销后可「重新签发」恢复。" />
      <Confirm open={confirm === "reset"} onClose={() => setConfirm(null)} onConfirm={() => action.mutate("reset")} loading={action.isPending} title="重置本期用量？" description="用量归零并开始新的周期；若状态为「流量用尽」会恢复为正常。" />
      <Confirm open={confirm === "reissue"} onClose={() => setConfirm(null)} onConfirm={() => action.mutate("reissue")} loading={action.isPending} title="重新签发？" description="保留各入口的 ID、端口、配置和用量，更换凭据与订阅链接。旧凭据和旧链接将失效。" />
      <Confirm open={confirm === "delete"} onClose={() => setConfirm(null)} onConfirm={() => delM.mutate()} loading={delM.isPending} destructive title="删除分享？" description="删除记录、专属节点、订阅与连接日志。不可恢复。" />
      <NodeDetailDialog node={nodeDetail} onClose={() => setNodeDetail(null)} />
    </div>
  );
}
