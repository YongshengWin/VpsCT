import * as React from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useSearchParams } from "react-router-dom";
import { ChevronLeft, ChevronRight, Download, Filter, Globe, MonitorSmartphone, Search, Trash2, UserRound, Waypoints, X } from "lucide-react";
import { del, get, put } from "@/lib/api";
import type { ConnClientHit, ConnEvent, Node, Server, Share, Subscription } from "@/lib/types";
import { cn, fmtBytes, fmtDate } from "@/lib/utils";
import { Button, Card, Confirm, Empty, Input, PageHeader, Select, Spinner, Switch } from "@/components/ui";
import { DateTimeInput } from "@/components/datetime-picker";
import { useToast } from "@/components/toast";
import { useAuth } from "@/lib/auth";

const PAGE = 100;

// ---------- protocol inference (agent only records tcp/udp + destination) ----------

type Tone = "green" | "yellow" | "purple" | "blue" | "cyan" | "orange" | "rose" | "gray";

const toneBadge: Record<Tone, string> = {
  green: "bg-emerald-500/20 text-emerald-700 dark:bg-emerald-400/25 dark:text-emerald-200",
  yellow: "bg-yellow-400/30 text-yellow-800 dark:bg-yellow-300/25 dark:text-yellow-100",
  purple: "bg-fuchsia-500/20 text-fuchsia-700 dark:bg-fuchsia-400/25 dark:text-fuchsia-100",
  blue: "bg-sky-500/20 text-sky-700 dark:bg-sky-400/25 dark:text-sky-100",
  cyan: "bg-cyan-500/20 text-cyan-700 dark:bg-cyan-400/25 dark:text-cyan-100",
  orange: "bg-orange-500/20 text-orange-700 dark:bg-orange-400/25 dark:text-orange-100",
  rose: "bg-rose-500/20 text-rose-700 dark:bg-rose-400/25 dark:text-rose-100",
  gray: "bg-foreground/10 text-foreground/70",
};
const toneDot: Record<Tone, string> = {
  green: "bg-emerald-500",
  yellow: "bg-yellow-400",
  purple: "bg-fuchsia-500",
  blue: "bg-sky-500",
  cyan: "bg-cyan-500",
  orange: "bg-orange-500",
  rose: "bg-rose-500",
  gray: "bg-muted-foreground/50",
};

const MAIL_PORTS = new Set([25, 110, 143, 465, 587, 993, 995]);

function protoOf(network: string, port: number): { label: string; tone: Tone } {
  const tcp = network === "tcp";
  if (port === 443) return tcp ? { label: "HTTPS", tone: "yellow" } : { label: "QUIC", tone: "purple" };
  if (tcp && (port === 80 || port === 8080)) return { label: "HTTP", tone: "blue" };
  if (port === 53) return { label: "DNS", tone: "cyan" };
  if (tcp && port === 853) return { label: "DoT", tone: "cyan" };
  if (tcp && port === 22) return { label: "SSH", tone: "orange" };
  if (tcp && MAIL_PORTS.has(port)) return { label: "MAIL", tone: "rose" };
  if (!tcp && (port === 3478 || port === 5349)) return { label: "STUN", tone: "purple" };
  return tcp ? { label: "TCP", tone: "green" } : { label: "UDP", tone: "purple" };
}

const pad = (n: number) => String(n).padStart(2, "0");
function fmtClock(iso: string): string {
  const d = new Date(iso);
  const now = new Date();
  const hms = `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
  const sameDay = d.getFullYear() === now.getFullYear() && d.getMonth() === now.getMonth() && d.getDate() === now.getDate();
  return sameDay ? hms : `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${hms}`;
}

// ---------- page ----------

export function ConnlogPage() {
  const { meta } = useAuth();
  const qc = useQueryClient();
  const toast = useToast();
  const [sp, setSp] = useSearchParams();
  const shareID = sp.get("share_id") ?? "";
  const serverID = sp.get("server_id") ?? "";
  const nodeID = sp.get("node_id") ?? "";
  const host = sp.get("host") ?? "";
  const src = sp.get("src") ?? "";
  const from = sp.get("from") ?? "";
  const to = sp.get("to") ?? "";
  const page = Number(sp.get("page") ?? "0");
  const setParam = (k: string, v: string) => {
    const n = new URLSearchParams(sp);
    if (v) n.set(k, v);
    else n.delete(k);
    if (k !== "page") n.delete("page");
    setSp(n, { replace: true });
  };
  const clearParams = (...keys: string[]) => {
    const n = new URLSearchParams(sp);
    for (const k of keys) n.delete(k);
    n.delete("page");
    setSp(n, { replace: true });
  };
  const [hostInput, setHostInput] = React.useState(host);
  React.useEffect(() => { setHostInput(host); }, [host]);
  const filterHost = (h: string) => {
    setHostInput(h);
    setParam("host", h === host ? "" : h);
  };
  const filterSrc = (ip: string) => setParam("src", ip === src ? "" : ip);
  const clearFilters = () => {
    setHostInput("");
    const n = new URLSearchParams(sp);
    for (const k of ["share_id", "server_id", "node_id", "host", "src", "from", "to", "page"]) n.delete(k);
    setSp(n, { replace: true });
  };

  const shares = useQuery({ queryKey: ["shares"], queryFn: () => get<Share[]>("/api/v1/shares") });
  const subscriptions = useQuery({ queryKey: ["subscriptions"], queryFn: () => get<Subscription[]>("/api/v1/subscriptions") });
  const servers = useQuery({ queryKey: ["servers"], queryFn: () => get<Server[]>("/api/v1/servers") });
  const nodes = useQuery({ queryKey: ["nodes", { source: "deployed" }], queryFn: () => get<Node[]>("/api/v1/nodes?source=deployed&include_revoked=1") });
  const qs = new URLSearchParams();
  if (shareID) qs.set("share_id", shareID);
  if (serverID) qs.set("server_id", serverID);
  if (nodeID) qs.set("node_id", nodeID);
  if (host) qs.set("host", host);
  if (src) qs.set("src", src);
  if (from) qs.set("from", new Date(from).toISOString());
  if (to) qs.set("to", new Date(to).toISOString());
  const filterQs = new URLSearchParams(qs);
  qs.set("limit", String(PAGE));
  qs.set("offset", String(page * PAGE));
  const events = useQuery({ queryKey: ["connlog", qs.toString()], queryFn: () => get<{ events: ConnEvent[]; total: number }>(`/api/v1/connlog?${qs}`), enabled: !!meta?.connlog, refetchInterval: 15000 });
  const summary = useQuery({
    queryKey: ["connlog", "summary", filterQs.toString()],
    queryFn: () => get<{ hosts: { key: string; hits: number }[]; clients: ConnClientHit[]; nodes: { key: string; hits: number }[]; shares: { key: string; hits: number }[] }>(`/api/v1/connlog/summary?${filterQs}`),
    enabled: !!meta?.connlog,
    refetchInterval: 15000,
  });
  const stats = useQuery({ queryKey: ["connlog", "stats"], queryFn: () => get<{ enabled: boolean; stats?: Record<string, number>; retention_days?: number; aggregate_retention_days?: number; self_enabled?: boolean }>("/api/v1/connlog/stats") });
  const [confirmPurge, setConfirmPurge] = React.useState(false);
  const purge = useMutation({
    mutationFn: () => del(`/api/v1/connlog/shares/${shareID}`),
    onSuccess: () => {
      toast.success("已清除该分享的连接日志");
      setConfirmPurge(false);
      qc.invalidateQueries({ queryKey: ["connlog"] });
    },
    onError: (e) => toast.fromError(e),
  });
  const toggleSelf = useMutation({
    mutationFn: (enabled: boolean) => put("/api/v1/settings", { "connlog.self_enabled": enabled ? "1" : "0" }),
    onSuccess: (_, enabled) => {
      toast.success(enabled ? "已开始记录自用" : "已停止记录自用");
      qc.invalidateQueries({ queryKey: ["connlog"] });
      qc.invalidateQueries({ queryKey: ["settings"] });
    },
    onError: (e) => toast.fromError(e),
  });
  const toggleShare = useMutation({
    mutationFn: ({ id, enabled }: { id: number; enabled: boolean }) => put<Share>(`/api/v1/shares/${id}/connlog`, { enabled }),
    onSuccess: (s) => {
      toast.success(s.connlog_enabled ? `已开始记录「${s.name}」` : `已停止记录「${s.name}」`);
      qc.invalidateQueries({ queryKey: ["shares"] });
      qc.invalidateQueries({ queryKey: ["connlog"] });
    },
    onError: (e) => toast.fromError(e),
  });

  const nodeName = (id: number) => nodes.data?.find((n) => n.id === id)?.name ?? `#${id}`;
  const serverName = (id: number) => servers.data?.find((s) => s.id === id)?.name ?? `#${id}`;
  const shareName = (id?: number | null) => (id ? shares.data?.find((s) => s.id === id)?.name ?? `#${id}` : "—");
  const selfSubNames = (subscriptions.data ?? []).filter((s) => s.kind !== "share" && s.enabled).map((s) => s.name);
  const shareSubName = (id: number) => subscriptions.data?.find((s) => s.share_id === id)?.name;
  const selfOn = stats.data?.self_enabled !== false;
  const total = events.data?.total ?? 0;
  const pages = Math.max(1, Math.ceil(total / PAGE));
  const hasFilter = !!(shareID || serverID || nodeID || host || src || from || to);

  if (meta && !meta.connlog) {
    return (
      <div>
        <PageHeader title="连接日志" />
        <Empty title="连接日志已在服务端禁用" description="ctlvpsd 以 --disable-connlog（或 CTLVPS_DISABLE_CONNLOG=1）启动。去掉该参数重启后即可记录自用与分享连接。" />
      </div>
    );
  }

  return (
    <div>
      <PageHeader
        title="连接日志"
        description="芯片点名字筛选来源，小开关控制是否记录。客户端 IP 由 agent 合成 sing-box 的 from/to；旧 agent 只有目标。"
        actions={
          <>
            <a href={`/api/v1/connlog/export?${qs}`} download>
              <Button variant="outline">
                <Download className="h-4 w-4" /> 导出 CSV
              </Button>
            </a>
            {shareID && (
              <Button variant="ghost" className="text-rose-600" onClick={() => setConfirmPurge(true)}>
                <Trash2 className="h-4 w-4" /> 清除该分享日志
              </Button>
            )}
          </>
        }
      />

      <Card className="mb-4 overflow-hidden p-3">
        <div className="mb-2 flex flex-wrap items-center gap-1.5 border-b border-border/60 pb-2">
          <span className="mr-0.5 text-xs text-muted-foreground">记录</span>
          <SourceChip
            label="自用"
            title={selfSubNames.length ? selfSubNames.join(" · ") : "自用节点"}
            enabled={selfOn}
            active={shareID === "self"}
            pending={toggleSelf.isPending}
            onToggle={(v) => toggleSelf.mutate(v)}
            onFilter={() => setParam("share_id", shareID === "self" ? "" : "self")}
          />
          {(shares.data ?? []).map((s) => (
            <SourceChip
              key={s.id}
              label={shareSubName(s.id) || s.name}
              title={shareSubName(s.id) && shareSubName(s.id) !== s.name ? s.name : "分享订阅"}
              enabled={s.connlog_enabled}
              active={shareID === String(s.id)}
              pending={toggleShare.isPending && toggleShare.variables?.id === s.id}
              onToggle={(v) => toggleShare.mutate({ id: s.id, enabled: v })}
              onFilter={() => setParam("share_id", shareID === String(s.id) ? "" : String(s.id))}
            />
          ))}
        </div>
        <div className="grid grid-cols-2 items-center gap-2 sm:flex sm:flex-wrap">
          <Select className={cn("w-full sm:w-40", shareID && "ring-2 ring-primary/40")} value={shareID} onChange={(e) => setParam("share_id", e.target.value)} aria-label="分享">
            <option value="">全部来源</option>
            <option value="self">自用</option>
            {(shares.data ?? []).map((s) => (
              <option key={s.id} value={s.id}>
                {s.name}
                {s.connlog_enabled ? "" : "（未开启）"}
              </option>
            ))}
          </Select>
          <Select className={cn("w-full sm:w-40", serverID && "ring-2 ring-primary/40")} value={serverID} onChange={(e) => setParam("server_id", e.target.value)} aria-label="服务器">
            <option value="">全部服务器</option>
            {(servers.data ?? []).map((s) => (
              <option key={s.id} value={s.id}>
                {s.name}
              </option>
            ))}
          </Select>
          <Select className={cn("col-span-2 w-full sm:col-span-1 sm:w-40", nodeID && "ring-2 ring-primary/40")} value={nodeID} onChange={(e) => setParam("node_id", e.target.value)} aria-label="节点">
            <option value="">全部节点</option>
            {(nodes.data ?? [])
              .filter((n) => !serverID || String(n.server_id) === serverID)
              .map((n) => (
                <option key={n.id} value={n.id}>
                  {n.name}
                </option>
              ))}
          </Select>
          <form
            className="relative col-span-2 min-w-[11rem] sm:flex-1"
            onSubmit={(e) => {
              e.preventDefault();
              filterHost(hostInput.trim());
            }}
          >
            <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
            <Input className={cn("pl-9 pr-9", host && "ring-2 ring-primary/40")} value={hostInput} onChange={(e) => setHostInput(e.target.value)} placeholder="搜索目标域名 / IP，回车过滤" />
            {host && (
              <button type="button" aria-label="清除搜索" onClick={() => filterHost("")} className="absolute right-2 top-1/2 -translate-y-1/2 rounded-full p-1 text-muted-foreground hover:bg-foreground/10 hover:text-foreground">
                <X className="h-3.5 w-3.5" />
              </button>
            )}
          </form>
          <DateTimeInput className={cn("w-full sm:w-44", from && "ring-2 ring-primary/40 rounded-md")} value={from} onChange={(v) => setParam("from", v)} placeholder="开始时间" />
          <DateTimeInput className={cn("w-full sm:w-44", to && "ring-2 ring-primary/40 rounded-md")} value={to} onChange={(v) => setParam("to", v)} placeholder="结束时间" />
        </div>
        {hasFilter && (
          <div className="-mx-3 -mb-3 mt-3 flex flex-wrap items-center gap-2 border-t border-primary/25 bg-primary/10 px-3 py-2.5">
            <span className="inline-flex items-center gap-1 text-xs font-semibold text-primary">
              <Filter className="h-3.5 w-3.5" /> 正在筛选
            </span>
            {shareID ? <FilterChip label={shareID === "self" ? "来源 · 自用" : `来源 · ${shareName(Number(shareID))}`} onClear={() => setParam("share_id", "")} /> : null}
            {serverID ? <FilterChip label={`服务器 · ${serverName(Number(serverID))}`} onClear={() => setParam("server_id", "")} /> : null}
            {nodeID ? <FilterChip label={`节点 · ${nodeName(Number(nodeID))}`} onClear={() => setParam("node_id", "")} /> : null}
            {host ? <FilterChip label={`目标 · ${host}`} onClear={() => filterHost("")} /> : null}
            {src ? <FilterChip label={`客户端 · ${src}`} onClear={() => setParam("src", "")} /> : null}
            {from || to ? <FilterChip label={`时间 · ${from || "…"} ~ ${to || "…"}`} onClear={() => clearParams("from", "to")} /> : null}
            <Button type="button" variant="outline" size="sm" className="ml-auto border-primary/40 bg-card text-primary hover:bg-primary/10" onClick={clearFilters}>
              <X className="h-3.5 w-3.5" /> 清除全部
            </Button>
          </div>
        )}
      </Card>

      {(summary.data?.hosts.length || summary.data?.clients.length) ? (
        <div className="mb-4 grid gap-3 lg:grid-cols-2">
          <Card className="p-3">
            <p className="mb-2 flex items-center gap-1.5 text-xs font-medium text-muted-foreground"><Globe className="h-3.5 w-3.5" /> 请求最多的地址</p>
            <div className="flex flex-col gap-1">
              {(summary.data?.hosts ?? []).slice(0, 8).map((h) => (
                <button key={h.key} type="button" onClick={() => filterHost(h.key)} className={cn("flex items-center justify-between gap-2 rounded-md px-1.5 py-1 text-left text-xs hover:bg-muted/60", host === h.key && "bg-primary/15 ring-1 ring-primary/40")}>
                  <span className="mono min-w-0 truncate">{h.key}</span>
                  <span className="tabular-nums text-muted-foreground">{h.hits.toLocaleString()}</span>
                </button>
              ))}
            </div>
          </Card>
          <Card className="p-3">
            <p className="mb-2 flex items-center gap-1.5 text-xs font-medium text-muted-foreground"><MonitorSmartphone className="h-3.5 w-3.5" /> 消耗最多的客户端</p>
            <div className="flex flex-col gap-1">
              {(summary.data?.clients ?? []).length ? (summary.data?.clients ?? []).slice(0, 8).map((h) => (
                <button key={h.key} type="button" onClick={() => filterSrc(h.key)} className={cn("flex items-center justify-between gap-2 rounded-md px-1.5 py-1 text-left text-xs hover:bg-muted/60", src === h.key && "bg-primary/15 ring-1 ring-primary/40")}>
                  <span className="min-w-0 truncate">
                    <span className="mono">{h.key}</span>
                    {h.geo?.label ? <span className="ml-1.5 font-normal text-muted-foreground">{h.geo.label}</span> : null}
                  </span>
                  <span className="tabular-nums text-muted-foreground">{h.hits.toLocaleString()}</span>
                </button>
              )) : <p className="px-1.5 py-2 text-xs text-muted-foreground">还没有客户端 IP。需要各 VPS 上的 agent 已更新：会把 sing-box 的 from（客户端）和 to（目标）合成一条记录。</p>}
            </div>
          </Card>
        </div>
      ) : null}

      <Card className="overflow-hidden">
          <div className="flex flex-wrap items-center justify-between gap-2 border-b border-border/60 px-4 py-2.5 dark:border-border">
            <div className="flex items-center gap-3 text-xs text-muted-foreground">
              <span>
                <span className="font-semibold text-foreground tabular-nums">{total.toLocaleString()}</span> 条{hasFilter ? "（已过滤）" : ""}
                {hasFilter ? (
                  <button type="button" className="ml-1.5 text-foreground/70 underline-offset-2 hover:underline" onClick={clearFilters}>
                    清除筛选
                  </button>
                ) : null}
              </span>
              {stats.data?.stats && (
                <span className="hidden sm:inline">
                  · 日聚合 {stats.data.stats.daily_rows?.toLocaleString() ?? 0} 行 · 库 {fmtBytes(stats.data.stats.db_bytes)}
                  {stats.data.retention_days ? ` · 原始保留 ${stats.data.retention_days} 天` : ""}
                  {stats.data.aggregate_retention_days ? ` / 聚合 ${stats.data.aggregate_retention_days} 天` : ""}
                </span>
              )}
            </div>
            <div className="flex items-center gap-1 text-xs tabular-nums text-muted-foreground">
              <Button size="icon" variant="ghost" className="h-7 w-7" disabled={page <= 0} onClick={() => setParam("page", String(page - 1))} aria-label="上一页">
                <ChevronLeft className="h-4 w-4" />
              </Button>
              {page + 1} / {pages}
              <Button size="icon" variant="ghost" className="h-7 w-7" disabled={page + 1 >= pages} onClick={() => setParam("page", String(page + 1))} aria-label="下一页">
                <ChevronRight className="h-4 w-4" />
              </Button>
            </div>
          </div>

          {events.isLoading ? (
            <Spinner />
          ) : !events.data?.events.length ? (
            <div className="px-6 py-14 text-center text-sm text-muted-foreground">
              {hasFilter ? "没有匹配的记录" : "还没有记录。用上方芯片开关按订阅开启；agent 需在线并已应用新配置。"}
            </div>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-xs">
                <thead>
                  <tr className="border-b border-border/60 text-left text-2xs font-semibold uppercase tracking-wider text-muted-foreground dark:border-border">
                    <th className="w-7 py-2 pl-4 pr-1" />
                    <th className="px-2 py-2">ID</th>
                    <th className="px-2 py-2">时间</th>
                    <th className="px-2 py-2">来源</th>
                    <th className="px-2 py-2">客户端</th>
                    <th className="px-2 py-2">节点</th>
                    <th className="px-2 py-2">协议</th>
                    <th className="px-2 py-2 pr-4">请求地址</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-border/40 dark:divide-border/60">
                  {events.data.events.map((e) => {
                    const p = protoOf(e.network, e.dest_port);
                    return (
                      <tr key={e.id} className="transition-colors hover:bg-muted/50">
                        <td className="py-1.5 pl-4 pr-1">
                          <span className={cn("block h-2 w-2 rounded-full", toneDot[p.tone])} />
                        </td>
                        <td className="mono px-2 py-1.5 tabular-nums text-muted-foreground">{e.id}</td>
                        <td className="mono whitespace-nowrap px-2 py-1.5 tabular-nums text-muted-foreground" title={fmtDate(e.ts)}>
                          {fmtClock(e.ts)}
                        </td>
                        <td className="whitespace-nowrap px-2 py-1.5">
                          {e.share_id ? (
                            <span className="inline-flex items-center gap-1.5">
                              <UserRound className="h-3.5 w-3.5 shrink-0 text-sky-500" />
                              {shareName(e.share_id)}
                            </span>
                          ) : (
                            <span className="text-muted-foreground">自用</span>
                          )}
                        </td>
                        <td className="whitespace-nowrap px-2 py-1.5">
                          {e.src_host ? (
                            <button className="hover:underline" onClick={() => filterSrc(e.src_host!)} title={src === e.src_host ? "取消客户端过滤" : "按此客户端过滤"}>
                              <span className="mono">{e.src_host}</span>
                              {e.src_geo?.label ? <span className="ml-1.5 font-sans text-muted-foreground">{e.src_geo.label}</span> : null}
                            </button>
                          ) : (
                            <span className="text-muted-foreground">—</span>
                          )}
                        </td>
                        <td className="whitespace-nowrap px-2 py-1.5">
                          <span className="inline-flex items-center gap-1.5">
                            <Waypoints className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                            {nodeName(e.node_id)}
                            <span className="text-muted-foreground">· {serverName(e.server_id)}</span>
                          </span>
                        </td>
                        <td className="px-2 py-1.5">
                          <span className={cn("inline-block rounded-md px-1.5 py-px text-2xs font-bold leading-4", toneBadge[p.tone])} title={`${e.network.toUpperCase()} · 按端口推断`}>
                            {p.label}
                          </span>
                        </td>
                        <td className="mono min-w-[14rem] break-all px-2 py-1.5 pr-4">
                          <button className="hover:underline" onClick={() => filterHost(e.dest_host)} title={host === e.dest_host ? "取消目标过滤" : "按此目标过滤"}>
                            {e.dest_host}
                          </button>
                          <span className="text-muted-foreground">:{e.dest_port}</span>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
      </Card>

      <Confirm open={confirmPurge} onClose={() => setConfirmPurge(false)} onConfirm={() => purge.mutate()} loading={purge.isPending} destructive title="清除该分享的全部连接日志？" description="全部事件都会删除，不可恢复。" />
    </div>
  );
}

function SourceChip({
  label,
  title,
  enabled,
  active,
  pending,
  onToggle,
  onFilter,
}: {
  label: string;
  title?: string;
  enabled: boolean;
  active: boolean;
  pending?: boolean;
  onToggle: (v: boolean) => void;
  onFilter: () => void;
}) {
  return (
    <div
      className={cn(
        "inline-flex max-w-full items-center gap-1.5 rounded-full border px-2 py-0.5",
        active ? "border-primary/40 bg-primary/10" : "border-border bg-muted/40",
        !enabled && !active && "opacity-70",
      )}
    >
      <button
        type="button"
        onClick={onFilter}
        title={title ? `${title} · 点名称筛选` : "点名称筛选"}
        className="max-w-[9rem] truncate text-xs font-medium"
      >
        {label}
      </button>
      <Switch size="sm" checked={enabled} onChange={onToggle} disabled={pending} aria-label={`记录${label}`} />
    </div>
  );
}

function FilterChip({ label, onClear }: { label: string; onClear: () => void }) {
  return (
    <button type="button" onClick={onClear} className="inline-flex max-w-full items-center gap-1.5 rounded-full border border-primary/30 bg-card px-2.5 py-1 text-xs font-medium text-foreground shadow-sm hover:border-primary hover:bg-primary/10" title="取消这项筛选">
      <span className="truncate">{label}</span>
      <X className="h-3.5 w-3.5 shrink-0 text-primary" />
    </button>
  );
}
