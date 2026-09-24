import * as React from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { ArrowUpCircle, X } from "lucide-react";
import { get } from "@/lib/api";
import { useAuth } from "@/lib/auth";
import { Button, Dialog, Input, Select } from "@/components/ui";
import { coreServerPage, type CoreServer, type CoreServerFilter, type CoreServerStatus } from "@/lib/core-upgrade";

type UpgradeStatus = {
  id: string;
  recommended_version: string;
  pinned_version: string;
  pin_compatible: boolean;
  needs_attention: boolean;
  servers: CoreServer[];
};

export function useCoreUpgrade() {
  const { user } = useAuth();
  return useQuery({ queryKey: ["core-upgrade"], queryFn: () => get<UpgradeStatus>("/api/v1/settings/core-upgrade"), enabled: user?.role === "admin", refetchInterval: 15_000 });
}

function dismissalKey(userID: number, noticeID: string) {
  return `ctlvps:core-notice:${userID}:${noticeID}`;
}

export function CoreUpgradeNotice() {
  const { user } = useAuth();
  const { data } = useCoreUpgrade();
  const [dismissed, setDismissed] = React.useState("");
  const key = user && data ? dismissalKey(user.id, data.id) : "";
  React.useEffect(() => {
    // Once the fleet has confirmed this recommendation, a later transient
    // outage must not turn it back into an upgrade announcement.
    if (key && data && data.servers.length > 0 && !data.needs_attention) {
      try { localStorage.setItem(key, "1"); } catch { /* Optional preference. */ }
      setDismissed(key);
    }
  }, [key, data]);
  let stored = false;
  try { stored = !!key && localStorage.getItem(key) === "1"; } catch { /* Storage may be unavailable. */ }
  if (!data?.needs_attention || !key || dismissed === key || stored) return null;
  const count = data.servers.filter(s => s.status !== "ready").length;
  return <aside aria-label="sing-box 升级提示" className="mb-5 flex items-start gap-3 rounded-xl border border-amber-500/25 bg-amber-500/5 p-4">
    <ArrowUpCircle className="mt-0.5 h-5 w-5 shrink-0 text-amber-600 dark:text-amber-400" />
    <div className="min-w-0 flex-1">
      <p className="text-sm font-semibold">{data.pin_compatible ? "sing-box 升级状态待确认" : "新功能需要升级 sing-box"}</p>
      <p className="mt-1 text-sm leading-6 text-muted-foreground">{data.pin_compatible ? `${count} 台服务器尚未确认完成，请查看实际版本与运行状态。` : `SS-2022 出口需要官方 ${data.recommended_version} 或兼容的 1.14.x。${count} 台服务器待检查；暂不使用此功能可关闭提示。`}</p>
      <Link to="/settings?tab=cores" className="mt-2 inline-block text-sm font-medium text-primary hover:underline">{data.pin_compatible ? "查看升级状态" : "查看升级建议"} →</Link>
    </div>
    <button type="button" aria-label="关闭本次内核升级提示" title="本浏览器不再提示同一条升级建议；设置页仍可查看" className="rounded-md p-1.5 text-muted-foreground hover:bg-accent focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary" onClick={() => {
      setDismissed(key);
      try { localStorage.setItem(key, "1"); } catch { /* Keep the current session dismissible. */ }
    }}><X className="h-4 w-4" /></button>
  </aside>;
}

const statusLabels = { upgrade: "待升级", pending: "等待应用", offline: "离线待确认", error: "需检查", ready: "已确认" };

export function CoreUpgradeDetails({ onSelect, selectedVersion }: { onSelect: (version: string) => void; selectedVersion: string }) {
  const { user } = useAuth();
  const q = useCoreUpgrade();
  const [filter, setFilter] = React.useState<CoreServerFilter | null>(null);
  const data = q.data;
  if (!data) return <div className="text-sm text-muted-foreground">{q.isError ? <>暂时无法获取内核状态。<button className="ml-2 text-primary" onClick={() => void q.refetch()}>重试</button></> : "正在核对内核版本…"}</div>;
  return <section aria-label="内核兼容检查" className="min-w-0">
    <div className="flex flex-wrap items-center justify-between gap-2">
      <h2 className="text-sm font-semibold">内核兼容检查</h2>
      <span className="text-xs text-muted-foreground">每 15 秒刷新</span>
    </div>
    <p className="mt-2 text-sm leading-6 text-muted-foreground">当前锁定 {data.pinned_version} · 本次验证版本 {data.recommended_version}（官方）。SS-2022 出口需兼容的 1.14.x，Mieru / Snell 使用各自独立内核。</p>
    {!data.pin_compatible && <Button type="button" variant="outline" className="mt-3" disabled={selectedVersion === data.recommended_version} onClick={() => onSelect(data.recommended_version)}>{selectedVersion === data.recommended_version ? "已选择，等待保存" : `选择官方 ${data.recommended_version}`}</Button>}
    <p className="mt-2 text-xs leading-5 text-muted-foreground">选择后需在版本选择区域点击「保存」才会更新。保存会影响使用该内核的受管服务器，重启期间连接可能短暂中断。</p>
    {data.servers.length > 0 ? <div className="mt-5">
      <p className="mb-2 text-sm font-medium">共 {data.servers.length} 台服务器</p>
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
        <button type="button" onClick={() => setFilter("all")} className="rounded-xl border border-border/70 p-3 text-left transition-colors hover:bg-accent/50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary"><span className="block text-xl font-semibold tabular-nums">{data.servers.length}</span><span className="text-xs text-muted-foreground">全部服务器</span></button>
        {(Object.keys(statusLabels) as CoreServerStatus[]).map(status => {
          const count = data.servers.filter(s => s.status === status).length;
          return <button key={status} type="button" disabled={!count} onClick={() => setFilter(status)} className="rounded-xl border border-border/70 p-3 text-left transition-colors hover:bg-accent/50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary disabled:cursor-default disabled:opacity-50">
            <span className="block text-xl font-semibold tabular-nums">{count}</span>
            <span className="text-xs text-muted-foreground">{statusLabels[status]}</span>
          </button>;
        })}
      </div>
      <div className="mt-3 flex flex-wrap gap-2">
        <Button variant="outline" onClick={() => setFilter("attention")} disabled={!data.servers.some(s => s.status !== "ready")}>查看待处理</Button>
        <Button variant="ghost" onClick={() => setFilter("all")}>查看全部服务器</Button>
      </div>
      {filter !== null && <CoreServerDialog servers={data.servers} initialFilter={filter} onClose={() => setFilter(null)} />}
    </div> : <p className="mt-3 text-sm text-muted-foreground">当前没有使用 sing-box 的受管服务器，无需升级。</p>}
    <p className="mt-3 text-xs leading-5 text-muted-foreground">根据服务器最新回报核对版本、配置应用和运行状态；离线或异常时不会标记完成。节点连通性仍需单独测试。</p>
    {data.needs_attention && user && <Link to="/" className="mt-2 inline-block text-xs text-primary hover:underline" onClick={() => {
      try { localStorage.removeItem(dismissalKey(user.id, data.id)); } catch { /* Optional preference. */ }
    }}>在总览重新显示本次升级提示</Link>}
  </section>;
}


function CoreServerDialog({ servers, initialFilter, onClose }: { servers: CoreServer[]; initialFilter: CoreServerFilter; onClose: () => void }) {
  const [query, setQuery] = React.useState("");
  const [filter, setFilter] = React.useState(initialFilter);
  const [page, setPage] = React.useState(1);
  const view = React.useMemo(() => coreServerPage(servers, query, filter, page), [servers, query, filter, page]);
  const [jump, setJump] = React.useState(String(view.page));
  React.useEffect(() => { setJump(String(view.page)); }, [view.page]);
  const jumpToPage = () => { const n = Number(jump); const target = Number.isFinite(n) ? Math.max(1, Math.min(view.pages, Math.trunc(n))) : view.page; setPage(target); setJump(String(target)); };
  return <Dialog open onClose={onClose} title="服务器内核状态" description="搜索服务器名称、编号或版本；异常优先显示，每页 8 台。" size="lg" footer={<>
    <span className="mr-auto text-xs text-muted-foreground" aria-live="polite">共 {view.total} 台 · 第 {view.page} / {view.pages} 页</span>
    <Button variant="outline" disabled={view.page <= 1} onClick={() => setPage(view.page - 1)}>上一页</Button>
    <Button variant="outline" disabled={view.page >= view.pages} onClick={() => setPage(view.page + 1)}>下一页</Button>
    <div className="flex items-center gap-2"><Input aria-label="跳转页码" type="number" min={1} max={view.pages} value={jump} onChange={e => setJump(e.target.value)} onKeyDown={e => { if (e.key === "Enter") jumpToPage(); }} className="w-16" /><Button variant="ghost" onClick={jumpToPage}>跳转</Button></div>
  </>}>
    <div className="mb-4 grid gap-2 sm:grid-cols-[1fr_10rem]">
      <Input autoFocus aria-label="搜索服务器" placeholder="搜索名称、编号或版本" value={query} onChange={e => { setQuery(e.target.value); setPage(1); }} />
      <Select aria-label="筛选内核状态" value={filter} onChange={e => { setFilter(e.target.value as CoreServerFilter); setPage(1); }}>
        <option value="all">全部状态</option><option value="attention">全部待处理</option>
        {(Object.keys(statusLabels) as CoreServerStatus[]).map(status => <option key={status} value={status}>{statusLabels[status]}</option>)}
      </Select>
    </div>
    <ul aria-label="服务器搜索结果" className="divide-y divide-border/60">
      {view.rows.map(s => <li key={s.id} className="flex items-center justify-between gap-3 py-3">
        <div className="min-w-0"><Link to={`/servers/${s.id}`} onClick={onClose} className="block break-words text-sm font-medium hover:text-primary">{s.name}</Link><p className="mt-1 text-xs text-muted-foreground">编号 {s.id} · {s.version || "版本未知"}</p></div>
        <span title={s.message} className={`shrink-0 text-xs ${s.status === "ready" ? "text-emerald-600 dark:text-emerald-400" : s.status === "error" ? "text-destructive" : "text-muted-foreground"}`}>{statusLabels[s.status]}</span>
      </li>)}
    </ul>
    {view.total === 0 && <div className="py-12 text-center text-sm text-muted-foreground"><p>没有符合条件的服务器</p><Button variant="ghost" className="mt-2" onClick={() => { setQuery(""); setFilter("all"); setPage(1); }}>清除筛选</Button></div>}
  </Dialog>;
}
