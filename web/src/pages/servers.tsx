import { ConfigStatusNotice, configurationState } from "@/components/config-status";
import * as React from "react";
import { MaintenancePanel } from "@/components/maintenance";
import { ServerActions } from "@/components/server-actions";
import { ServerNetwork } from "@/components/server-network";
import { ServerForwards } from "@/components/server-forwards";
import { ServerEgress } from "@/components/server-egress";
import { ServerRoutes } from "@/components/server-routes";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { Copy, KeyRound, Plus, RefreshCw, Trash2, Pencil, Cpu, MemoryStick, Wifi, ShieldCheck, AlertTriangle, Wrench } from "lucide-react";
import { get, post, put } from "@/lib/api";
import type { Server, Node, Series } from "@/lib/types";
import { fmtBytes, fmtAgo, fmtDuration, fmtRate, gbToBytes, bytesToGb, parseResetDay, copyText, STATUS_LABELS, PROTOCOL_LABELS, fmtDate, defaultNodeName } from "@/lib/utils";
import { Badge, Button, Card, CardContent, CardHeader, CardTitle, Confirm, Dialog, Empty, Field, Input, PageHeader, Progress, Select, Spinner, Switch, Table, Td, Th, Tr, Textarea, Code, Pre, Tabs } from "@/components/ui";
import { useToast } from "@/components/toast";
import { ResetDayInput } from "@/components/datetime-picker";
import { TrafficBars, RateArea } from "@/components/charts";
import { nicIO, TrafficIO } from "@/components/traffic-ways";
import { useAuth } from "@/lib/auth";
import { NodeRow, NodeDetailDialog } from "@/pages/nodes";

// ---------- shared form ----------
interface ServerForm {
  name: string;
  region: string;
  public_host: string;
  tags: string;
  notes: string;
  quota_gb: string;
  quota_reset_day: string;
  quota_billing: string;
  core_mode: string;
  ipv4_only: boolean;
  cert_mode: string;
  enabled: boolean;
}

const emptyForm: ServerForm = { name: "", region: "", public_host: "", tags: "", notes: "", quota_gb: "", quota_reset_day: "1", quota_billing: "dual", core_mode: "stable", ipv4_only: false, cert_mode: "self_signed", enabled: true };

function toForm(s: Server): ServerForm {
  return { name: s.name, region: s.region, public_host: s.public_host, tags: s.tags.join(","), notes: s.notes, quota_gb: bytesToGb(s.quota_bytes), quota_reset_day: String(s.quota_reset_day ?? 0), quota_billing: s.quota_billing, core_mode: s.core_mode, ipv4_only: s.ipv4_only, cert_mode: s.cert_mode, enabled: s.enabled };
}

function toPayload(f: ServerForm) {
  return { ...f, tags: f.tags.split(",").map((t) => t.trim()).filter(Boolean), quota_bytes: gbToBytes(f.quota_gb), quota_reset_day: parseResetDay(f.quota_reset_day) };
}

export function ServerDialog({ open, onClose, server }: { open: boolean; onClose: () => void; server?: Server }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [f, setF] = React.useState<ServerForm>(emptyForm);
  React.useEffect(() => setF(server ? toForm(server) : emptyForm), [server, open]);
  const set = <K extends keyof ServerForm>(k: K, v: ServerForm[K]) => setF((p) => ({ ...p, [k]: v }));
  const m = useMutation({
    mutationFn: () => (server ? put<Server>(`/api/v1/servers/${server.id}`, toPayload(f)) : post<Server>("/api/v1/servers", toPayload(f))),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["servers"] });
      toast.success(server ? "已保存" : "已添加服务器");
      onClose();
    },
    onError: (e) => toast.fromError(e),
  });
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={server ? "编辑服务器" : "添加服务器"}
      description="添加后生成注册令牌，在 VPS 上一键安装 agent。"
      footer={
        <>
          <Button variant="outline" onClick={onClose}>取消</Button>
          <Button onClick={() => m.mutate()} loading={m.isPending}>保存</Button>
        </>
      }
    >
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="名称"><Input value={f.name} onChange={(e) => set("name", e.target.value)} placeholder="hk-1" /></Field>
        <Field label="地区" hint="两位代码，如 HK/JP/US"><Input value={f.region} onChange={(e) => set("region", e.target.value.toUpperCase())} maxLength={4} /></Field>
        <Field label="公网地址" hint="留空则使用 agent 上报的 IP" className="sm:col-span-2"><Input value={f.public_host} onChange={(e) => set("public_host", e.target.value)} placeholder="1.2.3.4 或 hk.example.com" /></Field>
        <Field label="月流量配额 (GiB)" hint="0 为不限"><Input type="number" min={0} step="0.1" value={f.quota_gb} onChange={(e) => set("quota_gb", e.target.value)} /></Field>
        <Field label="重置日" hint="1–28 固定那天；29/30/31 都是每月最后一天。不选则按近 30 天滚动。"><ResetDayInput value={f.quota_reset_day} onChange={(v) => set("quota_reset_day", v)} /></Field>
        <Field label="内核模式" hint="精简模式不支持 Snell">
          <Select value={f.core_mode} onChange={(e) => set("core_mode", e.target.value)}>
            <option value="stable">稳定（sing-box + snell）</option>
            <option value="lean">精简（仅 sing-box）</option>
          </Select>
        </Field>
        <Field label="证书模式" hint="AnyTLS/Hy2/TUIC/Trojan 使用">
          <Select value={f.cert_mode} onChange={(e) => set("cert_mode", e.target.value)}>
            <option value="self_signed">自签名（客户端跳过验证）</option>
            <option value="acme">ACME 自动签发（需域名 + 80 端口）</option>
            <option value="external">外部证书文件</option>
          </Select>
        </Field>
        <Field label="标签" hint="逗号分隔"><Input value={f.tags} onChange={(e) => set("tags", e.target.value)} /></Field>
        <div className="flex flex-col gap-3 sm:col-span-2">
          <Switch checked={f.ipv4_only} onChange={(v) => set("ipv4_only", v)} label="出站仅 IPv4（避免 IPv6 抖动）" />
          <Switch checked={f.enabled} onChange={(v) => set("enabled", v)} label="启用（关闭后 agent 停止所有节点）" />
        </div>
        <Field label="备注" className="sm:col-span-2"><Textarea value={f.notes} onChange={(e) => set("notes", e.target.value)} rows={2} /></Field>
      </div>
    </Dialog>
  );
}

function AgentStatusBadge({ s }: { s: Server["agent_status"] }) {
  return <Badge variant={s === "online" ? "success" : s === "offline" ? "destructive" : "secondary"}>{STATUS_LABELS[s]}</Badge>;
}

// ---------- list ----------
export function ServersPage() {
  const q = useQuery({ queryKey: ["servers"], queryFn: () => get<Server[]>("/api/v1/servers"), refetchInterval: 15000 });
  const toast = useToast();
  const qc = useQueryClient();
  const [create, setCreate] = React.useState(false);
  const checkAgentUpdates = useMutation({
    mutationFn: () => post<{ message: string }>("/api/v1/agents/update"),
    onSuccess: (r) => { toast.success(r.message); qc.invalidateQueries({ queryKey: ["servers"] }); },
    onError: (e) => toast.fromError(e),
  });
  const stale = (q.data ?? []).filter((s) => s.agent_update?.outdated).length;
  return (
    <div>
      <PageHeader title="服务器" description="受 agent 管理的 VPS，按端口计量流量并部署节点" actions={
        <>
          {stale > 0 && (
            <Button variant="outline" onClick={() => checkAgentUpdates.mutate()} loading={checkAgentUpdates.isPending} title="检查与控制端提供的 agent 版本是否一致；有差异时会随心跳自动更新">
              <RefreshCw className="h-4 w-4" /> 检查 agent 更新（{stale} 台待同步）
            </Button>
          )}
          <Button onClick={() => setCreate(true)}><Plus className="h-4 w-4" /> 添加服务器</Button>
        </>
      } />
      {q.isLoading ? (
        <Spinner />
      ) : !q.data?.length ? (
        <Empty title="还没有服务器" description="添加一台 VPS，然后用生成的命令安装 agent。" action={<Button onClick={() => setCreate(true)}>添加服务器</Button>} />
      ) : (
        <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
          {q.data.map((s) => (
            <Link key={s.id} to={`/servers/${s.id}`} className="block min-w-0">
              <Card className="h-full p-4 transition-colors hover:bg-accent/30">
                <div className="flex items-start justify-between gap-2">
                  <div className="min-w-0">
                    <p className="break-words font-medium">{s.name}</p>
                    <p className="break-words text-xs text-muted-foreground">{s.public_host || s.agent?.public_ipv4 || "等待 agent 上报"} {s.region && `· ${s.region}`}</p>
                  </div>
                  <div className="flex shrink-0 flex-col items-end gap-1">
                    <AgentStatusBadge s={s.agent_status} />
                    {s.agent_update?.outdated ? <Badge variant="secondary">agent 待自动同步</Badge> : null}
                    {s.agent && !s.agent_update?.supported ? <Badge variant="secondary">需手动更新</Badge> : null}
                  </div>
                </div>
                <div className="mt-3 grid grid-cols-3 gap-2 text-xs text-muted-foreground">
                  <span className="inline-flex items-center gap-1"><Cpu className="h-3 w-3" /> {s.metrics ? `${s.metrics.cpu_percent.toFixed(0)}%` : "-"}</span>
                  <span className="inline-flex items-center gap-1"><MemoryStick className="h-3 w-3" /> {s.metrics?.mem_total ? `${((s.metrics.mem_used / s.metrics.mem_total) * 100).toFixed(0)}%` : "-"}</span>
                  <span className="inline-flex items-center gap-1"><Wifi className="h-3 w-3" /> {s.metrics ? fmtRate(s.metrics.net_rx_rate + s.metrics.net_tx_rate) : "-"}</span>
                </div>
                <div className="mt-3">
                  <div className="mb-1 flex justify-between text-xs text-muted-foreground">
                    <span>本期汇总 {fmtBytes(nicIO(s.usage?.up, s.usage?.down).total)}{s.quota_bytes > 0 && ` · 配额 ${fmtBytes(s.usage?.billed)} / ${fmtBytes(s.quota_bytes)}`}</span>
                    <span>{s.node_count} 节点</span>
                  </div>
                  <Progress value={s.quota_bytes > 0 ? s.usage?.percent ?? 0 : 0} />
                  <TrafficIO className="mt-1.5" compact inbound={s.usage?.inbound ?? s.usage?.up} outbound={s.usage?.outbound ?? s.usage?.down} />
                </div>
                {(s.desired && !s.desired.in_sync) || s.agent?.apply_error ? (
                  <p className="mt-2 inline-flex items-center gap-1 text-xs text-amber-600 dark:text-amber-400"><AlertTriangle className="h-3 w-3" /> {configurationState(s)?.title || "等待同步节点设置"}</p>
                ) : null}
              </Card>
            </Link>
          ))}
        </div>
      )}
      <ServerDialog open={create} onClose={() => setCreate(false)} />
    </div>
  );
}

// ---------- detail ----------
interface Revision {
  revision: number;
  hash: string;
  status: string;
  error: string;
  created_at: string;
  summary?: { nodes: unknown[] };
}

export function ServerDetailPage() {
  const { id } = useParams();
  const nav = useNavigate();
  const qc = useQueryClient();
  const toast = useToast();
  const { meta } = useAuth();
  const q = useQuery({ queryKey: ["servers", id], queryFn: () => get<Server>(`/api/v1/servers/${id}`), refetchInterval: 10000 });
  const [trafficDays, setTrafficDays] = React.useState(30);
  const [trafficSelection, setTrafficSelection] = React.useState({ serverID: id, nodeID: "server" });
  const nodes = useQuery({ queryKey: ["nodes", { server_id: id }], queryFn: () => get<Node[]>(`/api/v1/nodes?server_id=${id}&include_revoked=1`) });
  const trafficNode = trafficSelection.serverID === id
    ? nodes.data?.find((n) => String(n.id) === trafficSelection.nodeID)
    : undefined;
  const trafficSubject = trafficNode ? String(trafficNode.id) : "server";
  const traffic = useQuery({
    queryKey: ["servers", id, "traffic", trafficSubject, trafficDays],
    queryFn: () => get<Series>(trafficNode
      ? `/api/v1/nodes/${trafficNode.id}/traffic?days=${trafficDays}`
      : `/api/v1/servers/${id}/traffic?days=${trafficDays}`),
    refetchInterval: 30000,
  });
  const samples = useQuery({ queryKey: ["servers", id, "samples"], queryFn: () => get<{ ts: string; rx_rate: number; tx_rate: number }[]>(`/api/v1/servers/${id}/samples?hours=24`), refetchInterval: 60000 });
  const desired = useQuery({ queryKey: ["servers", id, "desired"], queryFn: () => get<Revision[]>(`/api/v1/servers/${id}/desired?limit=5`) });
  const [edit, setEdit] = React.useState(false);
  const [deploy, setDeploy] = React.useState(false);
  const [enroll, setEnroll] = React.useState<{ token: string; install_command: string; expires_at: string } | null>(null);
  const [confirmDel, setConfirmDel] = React.useState(false);
  const [confirmReset, setConfirmReset] = React.useState(false);
  const [searchParams, setSearchParams] = useSearchParams();
  type ServerTab = "nodes" | "routes" | "network" | "egress" | "forwards" | "diag" | "revisions";
  const selectedTab = searchParams.get("tab");
  const tab: ServerTab = ["nodes", "routes", "network", "egress", "forwards", "diag", "revisions"].includes(selectedTab ?? "") ? selectedTab as ServerTab : "nodes";
  const setTab = (value: ServerTab) => setSearchParams((previous) => { const next = new URLSearchParams(previous); next.set("tab", value); return next; }, { replace: true });
  const routeTab = tab === "routes" || tab === "network" || tab === "egress" || tab === "forwards";
  const [nodeDetail, setNodeDetail] = React.useState<Node | null>(null);
  const [maintenanceOpen, setMaintenanceOpen] = React.useState(false);
  const [maintenanceBusy, setMaintenanceBusy] = React.useState(false);

  const enrollM = useMutation({ mutationFn: () => post<{ token: string; install_command: string; expires_at: string }>(`/api/v1/servers/${id}/enroll-token`), onSuccess: setEnroll, onError: (e) => toast.fromError(e) });
  const republish = useMutation({ mutationFn: () => post(`/api/v1/servers/${id}/republish`), onSuccess: () => { toast.success("已重试，等待服务器同步"); qc.setQueryData<Server>(["servers", id], (old) => old?.desired ? { ...old, desired: { ...old.desired, in_sync: false, status: "pending", error: "" }, agent: old.agent ? { ...old.agent, apply_error: "" } : old.agent } : old); qc.invalidateQueries({ queryKey: ["servers", id] }); }, onError: (e) => toast.fromError(e) });
  const checkAgentUpdate = useMutation({
    mutationFn: () => post<{ queued?: boolean; manual?: boolean; message: string; agent_update?: { command?: string } }>(`/api/v1/servers/${id}/update-agent`),
    onSuccess: async (r) => {
      qc.invalidateQueries({ queryKey: ["servers", id] });
      if (r.manual && r.agent_update?.command) {
        await copyText(r.agent_update.command);
        toast.success(r.message + "（命令已复制）");
        return;
      }
      toast.success(r.message);
    },
    onError: (e) => toast.fromError(e),
  });
  const onServerDeleted = React.useCallback(() => { toast.success("服务器记录已删除"); void qc.invalidateQueries({ queryKey: ["servers"] }); void qc.invalidateQueries({ queryKey: ["nodes"] }); void qc.invalidateQueries({ queryKey: ["subscriptions"] }); nav("/servers"); }, [qc, nav]);
  const resetTok = useMutation({ mutationFn: () => post(`/api/v1/servers/${id}/reset-token`), onSuccess: () => { toast.success("已吊销 agent 令牌，需重新注册"); setConfirmReset(false); qc.invalidateQueries({ queryKey: ["servers", id] }); } });

  if (q.isLoading) return <Spinner />;
  const s = q.data;
  if (!s) return <Empty title="服务器不存在" />;
  const m = s.metrics;
  const d = s.diagnostics;

  return (
    <div>
      <PageHeader
        back={{ to: "/servers", label: "服务器" }}
        title={s.name}
        description={`${s.public_host || s.agent?.public_ipv4 || "—"} ${s.region ? `· ${s.region}` : ""} ${m?.hostname ? `· ${m.hostname}` : ""}`}
        actions={
          <>
            <AgentStatusBadge s={s.agent_status} />
            {s.agent_status === "pending" && <Button size="sm" onClick={() => enrollM.mutate()} loading={enrollM.isPending}><KeyRound className="h-4 w-4" /> 生成安装命令</Button>}
            <Button size="sm" variant="outline" onClick={() => setDeploy(true)}><Plus className="h-4 w-4" /> 部署节点</Button>
            <Button size="sm" variant="outline" onClick={() => setEdit(true)}><Pencil className="h-4 w-4" /> 编辑</Button>
            <ServerActions items={[
              { label: "agent 维护", icon: <Wrench className="h-4 w-4" />, onClick: () => setMaintenanceOpen(true) },
              ...(s.agent && !s.diagnostics?.maintenance ? [{ label: s.agent_update?.supported ? "检查 agent 更新" : "复制 agent 更新命令", icon: <Copy className="h-4 w-4" />, onClick: () => checkAgentUpdate.mutate(), disabled: checkAgentUpdate.isPending || maintenanceBusy }] : []),
              { label: "删除服务器记录", icon: <Trash2 className="h-4 w-4" />, onClick: () => setConfirmDel(true), disabled: maintenanceBusy, destructive: true },
            ]} />
          </>
        }
      />

      <MaintenancePanel key={s.id} server={{ id: s.id, name: s.name }} open={maintenanceOpen} onOpen={() => setMaintenanceOpen(true)} onClose={() => setMaintenanceOpen(false)} onBusyChange={setMaintenanceBusy} deleteOpen={confirmDel} onDeleteClose={() => setConfirmDel(false)} onDeleted={onServerDeleted} />

      {s.agent && <div className="mb-4 rounded-md border p-3 text-sm">安全状态：{s.agent_status === "pending" ? "尚未接入 agent" : !s.diagnostics?.security_version ? "旧版 agent，尚未启用新版安全策略" : !s.diagnostics.security_policy ? "本机安全策略加载失败，程序与配置变更已关闭" : s.diagnostics.security_paused ? "本机已暂停配置变更" : "更新文件校验与本机安全策略已启用"}</div>}
      {s.diagnostics?.metering_error && <div className="mb-4 rounded-md border border-red-500/40 p-3 text-sm text-destructive">节点流量采集异常：{s.diagnostics.metering_error}。当前用量可能未更新。</div>}
      {!maintenanceBusy && <ConfigStatusNotice server={s} retrying={republish.isPending} disabled={maintenanceBusy} onRetry={() => republish.mutate()} onDetails={() => setTab("diag")} />}
      {s.agent && s.agent_update && !s.agent_update.supported && (
        <div className="mb-4 rounded-md border border-amber-500/40 bg-amber-500/5 p-3 text-sm">
          请先在此 VPS 通过独立可信渠道配置验证器、签名根和本地安装器，再执行「更多」中的迁移命令。完成后只接受受信签名版本；未完成前不会自动更新。
        </div>
      )}
      {s.agent_update?.outdated && !s.diagnostics?.maintenance && (
        <div className="mb-4 rounded-md border border-sky-500/40 bg-sky-500/5 p-3 text-sm">
          agent 与控制端提供的版本不同，将随心跳自动同步。
        </div>
      )}

      <div className="mt-6">
        <Tabs value={routeTab ? "routes" : tab} onChange={setTab} items={[{ value: "nodes", label: `节点与概览 (${nodes.data?.filter((n) => !n.revoked).length ?? 0})` }, { value: "routes", label: "节点线路" }, { value: "diag", label: "诊断" }, { value: "revisions", label: "配置版本" }]} />
      </div>

      {tab === "nodes" && <>
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Card className="p-4"><p className="text-xs text-muted-foreground">CPU / 负载</p><p className="mt-1 text-xl font-semibold">{m ? `${m.cpu_percent.toFixed(0)}%` : "-"}</p><p className="text-xs text-muted-foreground">load {m?.load1?.toFixed(2) ?? "-"} / {m?.load5?.toFixed(2) ?? "-"}</p></Card>
        <Card className="p-4"><p className="text-xs text-muted-foreground">内存</p><p className="mt-1 text-xl font-semibold">{m?.mem_total ? `${((m.mem_used / m.mem_total) * 100).toFixed(0)}%` : "-"}</p><p className="text-xs text-muted-foreground">{fmtBytes(m?.mem_used)} / {fmtBytes(m?.mem_total)}</p></Card>
        <Card className="p-4"><p className="text-xs text-muted-foreground">磁盘</p><p className="mt-1 text-xl font-semibold">{m?.disk_total ? `${((m.disk_used / m.disk_total) * 100).toFixed(0)}%` : "-"}</p><p className="text-xs text-muted-foreground">{fmtBytes(m?.disk_used)} / {fmtBytes(m?.disk_total)}</p></Card>
        <Card className="p-4"><p className="text-xs text-muted-foreground">实时速率</p><p className="mt-1 text-xl font-semibold">{m ? fmtRate(m.net_rx_rate + m.net_tx_rate) : "-"}</p><p className="text-xs text-muted-foreground">入 {fmtRate(m?.net_rx_rate)} · 出 {fmtRate(m?.net_tx_rate)}</p></Card>
      </div>

      <div className="mt-4 grid gap-4 lg:grid-cols-3">
        <Card className="lg:col-span-2">
          <CardHeader>
            <CardTitle>服务器流量</CardTitle>
            <div className="grid gap-2 sm:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
              <Select aria-label="流量统计对象" value={trafficSubject} onChange={(e) => setTrafficSelection({ serverID: id, nodeID: e.target.value })}>
                <option value="server">整台服务器</option>
                {(nodes.data ?? []).map((n) => <option key={n.id} value={String(n.id)}>{n.name} · {PROTOCOL_LABELS[n.protocol] ?? n.protocol}{n.revoked ? "（已撤销）" : ""}</option>)}
              </Select>
              <Select aria-label="服务器流量时间范围" value={trafficDays} onChange={(e) => setTrafficDays(Number(e.target.value))}>
                {[7, 30, 90].map((days) => <option key={days} value={days}>近 {days} 天</option>)}
              </Select>
            </div>
          </CardHeader>
          <CardContent>
            {traffic.isLoading ? <p className="text-sm text-muted-foreground">正在加载流量…</p>
              : traffic.isError ? <p className="text-destructive">流量加载失败</p>
              : traffic.data?.has_data ? <><TrafficIO inbound={traffic.data.total_up} outbound={traffic.data.total_down} /><TrafficBars points={traffic.data.points} /></>
              : <p className="text-sm text-muted-foreground">暂无用量记录</p>}
            <p className="text-xs text-muted-foreground">{trafficNode
              ? "当前只显示所选节点的入站和出站流量。仅汇总已采集记录。"
              : "服务器按网卡收发计量，包含 SSH、系统更新等流量，与节点合计不必相等。仅汇总已采集记录。"}</p>
            {nodes.isError && <p className="mt-2 text-xs text-destructive">节点列表加载失败，暂时只能查看整台服务器。</p>}
          </CardContent>
        </Card>
        <Card>
          <CardHeader><CardTitle>本期配额</CardTitle></CardHeader>
          <CardContent>
            {s.usage && (
              <>
                <p className="text-2xl font-semibold tabular-nums">{fmtBytes(s.usage.total ?? nicIO(s.usage.up, s.usage.down).total)}</p>
                <p className="text-xs text-muted-foreground">本期汇总 · {s.quota_bytes > 0 ? `配额 ${fmtBytes(s.usage.billed)} / ${fmtBytes(s.quota_bytes)} · ${s.usage.percent.toFixed(1)}%` : "未设置配额"} · 自 {fmtDate(s.usage.period_start, false)}</p>
                <Progress className="mt-3" value={s.quota_bytes > 0 ? s.usage.percent : 0} />
                <TrafficIO className="mt-4" inbound={s.usage.inbound ?? s.usage.up} outbound={s.usage.outbound ?? s.usage.down} />
              </>
            )}
            <dl className="mt-4 space-y-1.5 text-xs text-muted-foreground">
              <div className="flex justify-between"><dt>agent 版本</dt><dd>{s.agent?.version || "-"}</dd></div>
              <div className="flex justify-between"><dt>最后心跳</dt><dd>{fmtAgo(s.agent?.last_seen_at)}</dd></div>
              <div className="flex justify-between"><dt>运行时间</dt><dd>{m ? fmtDuration(m.uptime_sec) : "-"}</dd></div>
              <div className="flex justify-between"><dt>配置版本</dt><dd>{s.desired ? `rev ${s.desired.revision} ${s.desired.in_sync ? "✓" : "…"}` : "-"}</dd></div>
              <div className="flex justify-between"><dt>内核</dt><dd>{m?.kernel || "-"} {m?.arch}</dd></div>
            </dl>
          </CardContent>
        </Card>
      </div>

      {samples.data && samples.data.length > 1 && (
        <Card className="mt-4">
          <CardHeader><CardTitle>近 24 小时速率</CardTitle></CardHeader>
          <CardContent><RateArea points={samples.data} /></CardContent>
        </Card>
      )}
      </>}

      {tab === "routes" && <ServerRoutes server={s} onNavigate={setTab} />}
      {routeTab && tab !== "routes" && <div className="mt-5">
        <Button size="sm" variant="outline" onClick={() => setTab("routes")}>← 返回节点线路</Button>
        <h2 className="mt-4 text-lg font-semibold">{tab === "network" ? "看网卡和流量" : tab === "egress" ? "连接已有代理" : "转发固定端口"}</h2>
      </div>}

      {tab === "nodes" && (
        <div className="mt-4">
          {!nodes.data?.length ? (
            <Empty title="尚未部署节点" description="点击「部署节点」在这台服务器上创建 VLESS Reality、Hysteria2、Snell 等入口。" action={<Button onClick={() => setDeploy(true)}>部署节点</Button>} />
          ) : (
            <Table>
              <thead><tr className="border-b"><Th>名称</Th><Th>协议</Th><Th>地址</Th><Th>归属</Th><Th>近 30 天用量</Th><Th>状态</Th><Th></Th></tr></thead>
              <tbody>{nodes.data.map((n) => <NodeRow key={n.id} n={n} onOpen={() => setNodeDetail(n)} configurationHint={!n.revoked && n.enabled ? configurationState(s, republish.isPending)?.title : undefined} />)}</tbody>
            </Table>
          )}
        </div>
      )}

      {tab === "network" && <ServerNetwork server={s} />}
      {tab === "egress" && <ServerEgress key={s.id} server={s} />}
      {tab === "forwards" && <ServerForwards key={s.id} server={s} />}

      {tab === "diag" && (
        <div className="mt-4 grid gap-4 lg:grid-cols-2">
          <Card>
            <CardHeader><CardTitle className="flex items-center gap-2"><ShieldCheck className="h-4 w-4" /> 主机健康</CardTitle></CardHeader>
            <CardContent>
              {!d ? <p className="text-sm text-muted-foreground">等待 agent 上报</p> : (
                <ul className="grid grid-cols-2 gap-2 text-sm">
                  <Diag ok={d.bbr} label={`拥塞控制 ${d.congestion_ctl || "?"}`} />
                  <Diag ok={d.time_sync} label="时间同步" />
                  <Diag ok={Math.abs(d.clock_skew_ms) < 2000} label={`时钟偏差 ${d.clock_skew_ms} ms`} />
                  <Diag ok={d.ipv4_reachable} label="IPv4 出网" />
                  <Diag ok={d.ipv6_reachable} label="IPv6 出网" warnOnly />
                  <Diag ok={d.nftables} label="nftables 计量" />
                  <Diag ok={d.systemd} label="systemd" />
                  <Diag ok={d.oom_events === 0} label={`OOM 事件 ${d.oom_events}`} />
                </ul>
              )}
              {d?.warnings?.length ? <ul className="mt-3 space-y-1 text-xs text-amber-600 dark:text-amber-400">{d.warnings.map((w, i) => <li key={i}>• {w}</li>)}</ul> : null}
              {d?.certs?.length ? (
                <div className="mt-4">
                  <p className="mb-1 text-xs font-medium text-muted-foreground">证书</p>
                  {d.certs.map((c) => <p key={c.domain} className="text-xs">{c.domain} · {c.mode} · 到期 {fmtDate(c.not_after, false)}</p>)}
                </div>
              ) : null}
            </CardContent>
          </Card>
          <Card>
            <CardHeader><CardTitle>内核进程</CardTitle></CardHeader>
            <CardContent>
              {!d?.cores?.length ? <p className="text-sm text-muted-foreground">无</p> : (
                <div className="space-y-3">
                  {d.cores.map((c) => (
                    <div key={c.name} className="rounded-md border p-3 text-sm">
                      <div className="flex items-center justify-between">
                        <span className="font-medium">{c.name} <span className="text-xs text-muted-foreground">{c.version}</span></span>
                        <Badge variant={!c.wanted ? "secondary" : c.active ? "success" : "destructive"}>{!c.wanted ? "未启用" : c.active ? "运行中" : "未运行"}</Badge>
                      </div>
                      <p className="mt-1 text-xs text-muted-foreground">
                        {c.installed ? "已安装" : "未安装"} · 内存 {fmtBytes(c.rss_bytes)} · 重启 {c.nrestarts} 次{c.instances ? ` · ${c.instances} 个实例` : ""}
                      </p>
                      {c.last_error && <p className="mt-1 break-all text-xs text-red-500">{c.last_error}</p>}
                    </div>
                  ))}
                </div>
              )}
              {d?.recent_errors?.length ? (
                <div className="mt-4">
                  <p className="mb-1 text-xs font-medium text-muted-foreground">sing-box 最近告警</p>
                  <Pre className="max-h-48">{d.recent_errors.join("\n")}</Pre>
                </div>
              ) : null}
              {d && d.connlog_lag > 0 && <p className="mt-3 text-xs text-muted-foreground">连接日志待上传：{d.connlog_lag} 条</p>}
            </CardContent>
          </Card>
        </div>
      )}

      {tab === "revisions" && (
        <Card className="mt-4">
          <CardContent className="pt-4">
            {!desired.data?.length ? <p className="text-sm text-muted-foreground">尚无配置版本</p> : (
              <Table>
                <thead><tr className="border-b"><Th>版本</Th><Th>状态</Th><Th>节点数</Th><Th>时间</Th><Th>错误</Th></tr></thead>
                <tbody>
                  {desired.data.map((r) => (
                    <Tr key={r.revision}>
                      <Td>rev {r.revision}</Td>
                      <Td><Badge variant={r.status === "applied" ? "success" : r.status === "failed" ? "destructive" : "secondary"}>{r.status}</Badge></Td>
                      <Td>{r.summary?.nodes?.length ?? "-"}</Td>
                      <Td className="text-muted-foreground">{fmtDate(r.created_at)}</Td>
                      <Td className="max-w-md break-words text-xs text-red-500">{r.error}</Td>
                    </Tr>
                  ))}
                </tbody>
              </Table>
            )}
            <div className="mt-4 flex flex-wrap gap-2">
              <Button size="sm" variant="outline" onClick={() => enrollM.mutate()} loading={enrollM.isPending}><KeyRound className="h-4 w-4" /> 重新生成安装命令</Button>
              {s.agent && (
                <Button size="sm" variant="outline" onClick={async () => {
                  const origin = window.location.origin;
                  const cmd = `curl -fsSL ${origin}/install-agent.sh | sudo bash -s -- --update --server ${origin}`;
                  await copyText(cmd);
                  toast.success("已复制更新命令，在该 VPS 上执行即可，不用重新注册");
                }}><RefreshCw className="h-4 w-4" /> 复制更新 agent 命令</Button>
              )}
              <Button size="sm" variant="outline" className="text-red-500" onClick={() => setConfirmReset(true)}>吊销 agent 令牌</Button>
            </div>
          </CardContent>
        </Card>
      )}

      <ServerDialog open={edit} onClose={() => setEdit(false)} server={s} />
      <DeployDialog open={deploy} onClose={() => setDeploy(false)} server={s} protocols={meta?.protocols ?? []} />
      <Dialog open={!!enroll} onClose={() => setEnroll(null)} title="安装 agent" description="先从独立可信发行渠道安装验证器、安装脚本和本机策略，再以 root 执行以下命令。令牌 15 分钟有效，仅可使用一次。">
        {enroll && (
          <div className="space-y-3">
            <Pre className="whitespace-pre-wrap break-all">{enroll.install_command}</Pre>
            <Button onClick={async () => { await copyText(enroll.install_command); toast.success("已复制"); }}><Copy className="h-4 w-4" /> 复制命令</Button>
            <p className="text-xs text-muted-foreground">脚本会安装 nftables/chrony，下载 ctlvps-agent，注册并以 systemd 常驻。安装完成后此页面会显示「在线」。</p>
          </div>
        )}
      </Dialog>
      <Confirm open={confirmReset} onClose={() => setConfirmReset(false)} onConfirm={() => resetTok.mutate()} destructive title="吊销 agent 令牌？" description="agent 将无法继续通信，需要重新生成安装命令并注册。" />
      <NodeDetailDialog node={nodeDetail} onClose={() => setNodeDetail(null)} />
    </div>
  );
}

function Diag({ ok, label, warnOnly }: { ok: boolean; label: string; warnOnly?: boolean }) {
  return (
    <li className="flex items-center gap-2">
      <span className={`h-2 w-2 rounded-full ${ok ? "bg-emerald-500" : warnOnly ? "bg-amber-500" : "bg-red-500"}`} />
      {label}
    </li>
  );
}

function DeployDialog({ open, onClose, server, protocols }: { open: boolean; onClose: () => void; server: Server; protocols: string[] }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [f, setF] = React.useState({ protocol: "vless", name: "", port: "", sni: "", domain: "", obfs: false, snell_version: 4, mieru_transport: "TCP", cert_mode: "", cert_id: "" });
  const set = (k: string, v: unknown) => setF((p) => ({ ...p, [k]: v }));
  const m = useMutation({
    mutationFn: () => post<Node>(`/api/v1/servers/${server.id}/nodes`, { ...f, port: Number(f.port) || 0, snell_version: Number(f.snell_version) }),
    onSuccess: () => { toast.success("节点已创建，agent 将在下次心跳后生效"); qc.invalidateQueries({ queryKey: ["nodes"] }); qc.invalidateQueries({ queryKey: ["servers"] }); onClose(); },
    onError: (e) => toast.fromError(e),
  });
  const list = protocols.filter((p) => !(server.core_mode === "lean" && ["snell","mieru"].includes(p)));
  const tls = ["anytls", "hysteria2", "tuic", "trojan"].includes(f.protocol);
  return (
    <Dialog open={open} onClose={onClose} title={`在 ${server.name} 上部署节点`} description="凭据自动生成；Reality 无需证书，Hy2/TUIC/AnyTLS/Trojan 按服务器证书模式处理。" footer={<><Button variant="outline" onClick={onClose}>取消</Button><Button onClick={() => m.mutate()} loading={m.isPending}>部署</Button></>}>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="协议">
          <Select value={f.protocol} onChange={(e) => set("protocol", e.target.value)}>
            {list.map((p) => <option key={p} value={p}>{PROTOCOL_LABELS[p] ?? p}</option>)}
          </Select>
        </Field>
        <Field label="名称" hint="留空自动生成"><Input value={f.name} onChange={(e) => set("name", e.target.value)} placeholder={defaultNodeName(server, f.protocol)} /></Field>
        <Field label="端口" hint="留空随机 20000-50000"><Input type="number" value={f.port} onChange={(e) => set("port", e.target.value)} /></Field>
        {f.protocol === "vless" && <Field label="Reality 伪装域名" hint="默认 www.sony.com"><Input value={f.sni} onChange={(e) => set("sni", e.target.value)} placeholder="www.sony.com" /></Field>}
        {tls && (
          <>
            <Field label="TLS 域名" hint="留空用服务器公网地址"><Input value={f.domain} onChange={(e) => set("domain", e.target.value)} /></Field>
            <Field label="证书模式" hint="留空继承服务器">
              <Select value={f.cert_mode} onChange={(e) => set("cert_mode", e.target.value)}>
                <option value="">继承（{server.cert_mode}）</option>
                <option value="self_signed">自签名</option>
                <option value="acme">ACME</option>
                <option value="external">外部证书</option>
              </Select>
            </Field>
          </>
        )}
        {tls && (f.cert_mode || server.cert_mode) === "external" && <Field label="外部证书 ID" hint="填写此 VPS 本机安全策略中已登记的证书名称"><Input value={f.cert_id} onChange={(e) => set("cert_id", e.target.value)} maxLength={64} /></Field>}
        {f.protocol === "hysteria2" && <div className="sm:col-span-2"><Switch checked={f.obfs} onChange={(v) => set("obfs", v)} label="启用 Salamander 混淆（对抗 QUIC 封锁）" /></div>}
        {f.protocol === "wireguard" && <p className="text-sm text-muted-foreground">每个节点使用独立端口和单用户密钥，可在节点详情导出标准 WireGuard 配置。用户态接入不创建宿主机 VPN 或子网路由。</p>}
        {f.protocol === "mieru" && <Field label="mieru 传输" hint="独立 mita 实例，支持 mihomo。"><Select value={f.mieru_transport} onChange={e=>set("mieru_transport",e.target.value)}><option>TCP</option><option>UDP</option></Select></Field>}
        {f.protocol === "snell" && (
          <Field label="Snell 版本">
            <Select value={String(f.snell_version)} onChange={(e) => set("snell_version", Number(e.target.value))}>
              <option value="4">v4（兼容最广）</option>
              <option value="5">v5（Surge 5.x+）</option>
            </Select>
          </Field>
        )}
      </div>
      <p className="mt-4 text-xs text-muted-foreground">提示：TCP+QUIC 双入口更稳——同一台机器同时部署 VLESS Reality 与 Hysteria2，客户端用 <Code>url-test</Code> 组自动切换。</p>
    </Dialog>
  );
}
