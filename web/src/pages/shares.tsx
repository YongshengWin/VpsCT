import * as React from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Pause, Play, Plus, RotateCcw, Trash2, Pencil, Ban, KeyRound, ScrollText } from "lucide-react";
import { del, get, post, put } from "@/lib/api";
import type { Share, ShareStatus, Server, Node, RuleTemplate, User, Series, ShareTarget } from "@/lib/types";
import { cn, displayName, fmtBytes, fmtDate, fmtAgo, gbToBytes, bytesToGb, parseResetDay, STATUS_LABELS, PROTOCOL_LABELS } from "@/lib/utils";
import { Badge, Button, Card, CardContent, CardHeader, CardTitle, Confirm, Dialog, Empty, Field, Input, PageHeader, Progress, Select, Spinner, Switch, Table, Th, Textarea } from "@/components/ui";
import { DateTimeInput, ResetDayInput } from "@/components/datetime-picker";
import { useToast } from "@/components/toast";
import { useAuth, useIsAdmin } from "@/lib/auth";
import { TrafficBars } from "@/components/charts";
import { nicIO, TrafficIO } from "@/components/traffic-ways";
import { SubscriptionLinks } from "@/pages/subscriptions";
import { NodeRow, NodeDetailDialog } from "@/pages/nodes";

export function StatusBadge({ s }: { s: ShareStatus }) {
  const v = s === "active" ? "success" : s === "paused" ? "warning" : s === "exhausted" ? "destructive" : "secondary";
  return <Badge variant={v}>{STATUS_LABELS[s] ?? s}</Badge>;
}

// ShareCard is used on the dashboard (user view) and in the list.
export function ShareCard({ share, link = true }: { share: Share; link?: boolean }) {
  const u = share.usage;
  const body = (
    <Card className="h-full p-4 transition-colors hover:bg-accent/30">
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <p className="break-words font-medium">{share.name}</p>
          <p className="break-words text-xs text-muted-foreground">{share.user_name ? `用户 ${share.user_name} · ` : ""}{share.targets.length} 台服务器 · {share.targets.reduce((a, t) => a + t.protocols.length, 0) + share.extra_node_ids.length} 节点</p>
        </div>
        <StatusBadge s={share.status} />
      </div>
      <div className="mt-3">
        <div className="mb-1 flex justify-between text-xs text-muted-foreground">
          <span>本期汇总 {fmtBytes(u.total ?? nicIO(share.used_upload, share.used_download).total)}{u.quota > 0 && ` · 配额 ${fmtBytes(u.used)} / ${fmtBytes(u.quota)}`}</span>
          <span>{u.quota > 0 ? `${u.percent.toFixed(0)}%` : "不限量"}</span>
        </div>
        <Progress value={u.quota > 0 ? u.percent : 0} />
        <TrafficIO className="mt-1.5" compact inbound={u.inbound ?? share.used_upload} outbound={u.outbound ?? share.used_download} />
        <p className="mt-1 text-xs text-muted-foreground">
          {u.next_reset ? `下次重置 ${fmtDate(u.next_reset, false)}` : "不重置"}
          {share.expires_at && ` · 到期 ${fmtDate(share.expires_at, false)}`}
        </p>
      </div>
    </Card>
  );
  return link ? <Link to={`/shares/${share.id}`} className="block min-w-0">{body}</Link> : body;
}

export function SharesPage() {
  const admin = useIsAdmin();
  const q = useQuery({ queryKey: ["shares"], queryFn: () => get<Share[]>("/api/v1/shares"), refetchInterval: 30000 });
  const [create, setCreate] = React.useState(false);
  const [filter, setFilter] = React.useState<"" | ShareStatus>("");
  const list = (q.data ?? []).filter((s) => !filter || s.status === filter);
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
  billing_mode: string;
  reset_day: string;
  expires_at: string;
  template_id: number;
  connlog_enabled: boolean;
  notes: string;
}
const emptyForm: Form = { name: "", user_id: 0, targets: [], extra_node_ids: [], quota_gb: "100", billing_mode: "dual", reset_day: "1", expires_at: "", template_id: 0, connlog_enabled: false, notes: "" };

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
  React.useEffect(() => {
    setF(share ? { name: share.name, user_id: share.user_id ?? 0, targets: share.targets ?? [], extra_node_ids: share.extra_node_ids ?? [], quota_gb: bytesToGb(share.quota_bytes), billing_mode: share.billing_mode, reset_day: String(share.reset_day ?? 0), expires_at: share.expires_at ? toLocal(share.expires_at) : "", template_id: share.template_id ?? 0, connlog_enabled: share.connlog_enabled, notes: share.notes } : emptyForm);
  }, [share, open]);
  const set = <K extends keyof Form>(k: K, v: Form[K]) => setF((p) => ({ ...p, [k]: v }));
  const m = useMutation({
    mutationFn: () => {
      const payload = { ...f, user_id: f.user_id || null, quota_bytes: gbToBytes(f.quota_gb), reset_day: parseResetDay(f.reset_day), template_id: f.template_id || null, expires_at: f.expires_at ? new Date(f.expires_at).toISOString() : "0001-01-01T00:00:00Z" };
      return share ? put<Share>(`/api/v1/shares/${share.id}`, payload) : post<Share>("/api/v1/shares", payload);
    },
    onSuccess: (r) => {
      qc.invalidateQueries({ queryKey: ["shares"] }); qc.invalidateQueries({ queryKey: ["nodes"] }); qc.invalidateQueries({ queryKey: ["subscriptions"] });
      toast.success(share ? "已保存，节点变更将在下次心跳生效" : "分享已创建");
      onClose();
      if (!share) nav(`/shares/${r.id}`);
    },
    onError: (e) => toast.fromError(e),
  });
  const toggleProto = (serverID: number, proto: string, on: boolean) => {
    const t = f.targets.find((x) => x.server_id === serverID);
    let next: ShareTarget[];
    if (!t) next = on ? [...f.targets, { server_id: serverID, protocols: [proto] }] : f.targets;
    else {
      const protocols = on ? Array.from(new Set([...t.protocols, proto])) : t.protocols.filter((p) => p !== proto);
      next = protocols.length ? f.targets.map((x) => (x.server_id === serverID ? { ...x, protocols } : x)) : f.targets.filter((x) => x.server_id !== serverID);
    }
    set("targets", next);
  };
  const extraCandidates = (nodes.data ?? []).filter((n) => !n.share_id && !n.revoked && n.source !== "deployed");
  const enabledServers = (servers.data ?? []).filter((s) => s.enabled);
  const entryCount = f.targets.reduce((n, t) => n + t.protocols.length, 0);
  // Single column sized to the content: the old two-column layout left the
  // right half empty whenever there were no servers / spare nodes.
  return (
    <Dialog open={open} onClose={onClose} title={share ? "编辑分享" : "新建分享"} size="md" description="每个勾选的「服务器 × 协议」会生成一个独立端口与凭据的专属入口，流量按端口计量。"
      footer={<><Button variant="outline" onClick={onClose}>取消</Button><Button onClick={() => m.mutate()} loading={m.isPending}>{share ? "保存" : "创建"}</Button></>}>
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
                const protos = (meta?.protocols ?? []).filter((p) => !(s.core_mode === "lean" && p === "snell"));
                return (
                  <div key={s.id} className="rounded-xl border p-3">
                    <div className="mb-2 flex items-center justify-between gap-2">
                      <p className="min-w-0 break-words text-sm font-medium">{s.name} <span className="text-xs text-muted-foreground">{s.region}{s.region ? " · " : ""}{s.agent_status === "online" ? "在线" : STATUS_LABELS[s.agent_status]}</span></p>
                      {t && <Badge variant="info">{t.protocols.length}</Badge>}
                    </div>
                    <div className="flex flex-wrap gap-1.5">
                      {protos.map((p) => (
                        <label key={p} className={cn("inline-flex cursor-pointer items-center gap-1 rounded-md border px-2 py-1 text-xs", t?.protocols.includes(p) && "border-primary/40 bg-primary/10 text-primary")}>
                          <input type="checkbox" className="accent-primary" checked={!!t?.protocols.includes(p)} onChange={(e) => toggleProto(s.id, p, e.target.checked)} />
                          {PROTOCOL_LABELS[p] ?? p}
                        </label>
                      ))}
                    </div>
                  </div>
                );
              })}
            </div>
          )}
        </section>

        {extraCandidates.length > 0 && (
          <section>
            <p className="mb-1 text-sm font-medium">附加节点</p>
            <p className="mb-2 text-xs text-muted-foreground">导入/手动节点无法硬性限流，用量仅按订阅侧估算，建议只作备用。</p>
            <div className="max-h-44 overflow-auto rounded-xl border">
              {extraCandidates.map((n) => (
                <label key={n.id} className="flex items-center gap-2 border-b px-3 py-1.5 text-sm last:border-0">
                  <input type="checkbox" className="accent-primary" checked={f.extra_node_ids.includes(n.id)} onChange={(e) => set("extra_node_ids", e.target.checked ? [...f.extra_node_ids, n.id] : f.extra_node_ids.filter((x) => x !== n.id))} />
                  <span className="min-w-0 flex-1 break-words">{n.name}</span><span className="text-xs text-muted-foreground">{PROTOCOL_LABELS[n.protocol] ?? n.protocol}</span>
                </label>
              ))}
            </div>
          </section>
        )}

        <section className="space-y-3">
          <p className="text-sm font-medium">配额与有效期</p>
          <div className="grid grid-cols-2 gap-3">
            <Field label="配额 (GiB)" hint="0 不限，按入站+出站汇总"><Input type="number" min={0} step="0.1" value={f.quota_gb} onChange={(e) => set("quota_gb", e.target.value)} /></Field>
            <Field label="重置日" hint="1–28 固定那天；29/30/31 都是每月最后一天。"><ResetDayInput value={f.reset_day} onChange={(v) => set("reset_day", v)} /></Field>
          </div>
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
  const nav = useNavigate();
  const qc = useQueryClient();
  const toast = useToast();
  const q = useQuery({ queryKey: ["shares", id], queryFn: () => get<Share>(`/api/v1/shares/${id}`), refetchInterval: 20000 });
  const traffic = useQuery({ queryKey: ["shares", id, "traffic"], queryFn: () => get<Series>(`/api/v1/shares/${id}/traffic?days=30`) });
  const events = useQuery({ queryKey: ["shares", id, "events"], queryFn: () => get<{ id: number; ts: string; kind: string; detail: string }[]>(`/api/v1/shares/${id}/events?limit=50`) });
  const [edit, setEdit] = React.useState(false);
  const [confirm, setConfirm] = React.useState<null | "revoke" | "delete" | "reset" | "reissue">(null);
  const [nodeDetail, setNodeDetail] = React.useState<Node | null>(null);
  const invalidate = () => { qc.invalidateQueries({ queryKey: ["shares"] }); qc.invalidateQueries({ queryKey: ["nodes"] }); qc.invalidateQueries({ queryKey: ["subscriptions"] }); };
  const action = useMutation({
    mutationFn: (a: string) => post<{ share: Share; token?: string }>(`/api/v1/shares/${id}/${a}`),
    onSuccess: (_r, a) => { toast.success({ pause: "已暂停", resume: "已恢复", reset: "用量已重置", revoke: "已撤销", reissue: "已重新签发：新端口、新凭据、新订阅链接" }[a] ?? "完成"); setConfirm(null); invalidate(); },
    onError: (e) => toast.fromError(e),
  });
  const delM = useMutation({ mutationFn: () => del(`/api/v1/shares/${id}`), onSuccess: () => { toast.success("已删除"); invalidate(); nav("/shares"); }, onError: (e) => toast.fromError(e) });
  if (q.isLoading) return <Spinner />;
  const s = q.data;
  if (!s) return <Empty title="分享不存在" />;
  const u = s.usage;
  return (
    <div>
      <PageHeader
        back={{ to: "/shares", label: "分享" }}
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
            <p className="text-2xl font-semibold tabular-nums">{fmtBytes(u.total ?? nicIO(s.used_upload, s.used_download).total)}</p>
            <p className="text-xs text-muted-foreground">本期汇总 · {u.quota > 0 ? `配额 ${fmtBytes(u.used)} / ${fmtBytes(u.quota)} · 剩余 ${fmtBytes(Math.max(0, u.remaining))}` : "不限量"} · 自 {fmtDate(s.period_start, false)}</p>
            <Progress className="mt-3" value={u.quota > 0 ? u.percent : 0} />
            <TrafficIO className="mt-4" inbound={u.inbound ?? s.used_upload} outbound={u.outbound ?? s.used_download} />
            <dl className="mt-4 space-y-1.5 text-xs text-muted-foreground">
              <div className="flex justify-between"><dt>下次重置</dt><dd>{u.next_reset ? fmtDate(u.next_reset, false) : "不重置"}</dd></div>
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
      <Card className="mt-4">
        <CardHeader><CardTitle>近 30 天流量（柱为入站/出站）</CardTitle></CardHeader>
        <CardContent><TrafficBars points={traffic.data?.points ?? []} /></CardContent>
      </Card>
      <div className="mt-4 grid gap-4 lg:grid-cols-3">
        <Card className="lg:col-span-2">
          <CardHeader><CardTitle>专属节点（{s.nodes?.length ?? 0}）</CardTitle></CardHeader>
          <CardContent>
            {!s.nodes?.length ? <p className="text-sm text-muted-foreground">无</p> : (
              <Table>
                <thead><tr className="border-b"><Th>名称</Th><Th>协议</Th><Th>地址</Th><Th>归属</Th><Th>近 30 天用量</Th><Th>状态</Th><Th></Th></tr></thead>
                <tbody>{s.nodes.map((n) => <NodeRow key={n.id} n={n} onOpen={() => setNodeDetail(n)} />)}</tbody>
              </Table>
            )}
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
      </div>
      <ShareDialog open={edit} onClose={() => setEdit(false)} share={s} />
      <Confirm open={confirm === "revoke"} onClose={() => setConfirm(null)} onConfirm={() => action.mutate("revoke")} loading={action.isPending} destructive title="撤销分享？" description="专属入口从服务器上撤下、订阅链接失效。撤销后可「重新签发」恢复。" />
      <Confirm open={confirm === "reset"} onClose={() => setConfirm(null)} onConfirm={() => action.mutate("reset")} loading={action.isPending} title="重置本期用量？" description="用量归零并开始新的周期；若状态为「流量用尽」会恢复为正常。" />
      <Confirm open={confirm === "reissue"} onClose={() => setConfirm(null)} onConfirm={() => action.mutate("reissue")} loading={action.isPending} title="重新签发？" description="所有专属入口更换端口与凭据，订阅链接更换。用于对方泄露链接时止损。" />
      <Confirm open={confirm === "delete"} onClose={() => setConfirm(null)} onConfirm={() => delM.mutate()} loading={delM.isPending} destructive title="删除分享？" description="删除记录、专属节点、订阅与连接日志。不可恢复。" />
      <NodeDetailDialog node={nodeDetail} onClose={() => setNodeDetail(null)} />
    </div>
  );
}