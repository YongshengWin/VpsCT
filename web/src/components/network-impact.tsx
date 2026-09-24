import * as React from "react";
import type { NetworkImpact } from "@/lib/network";
import { Badge, Button } from "@/components/ui";

const effectLabels: Record<NetworkImpact["nodes"][number]["effect"], string> = {
  binding: "修改网络", clear: "恢复默认网络", disable: "停用出口", resume: "恢复原出口", pinned: "保留固定版本", restart: "可能随进程重启",
};

export function NetworkImpactPreview({ impact }: { impact: NetworkImpact }) {
  const [page, setPage] = React.useState(0);
  const [historyPage, setHistoryPage] = React.useState(0);
  const pages = Math.max(1, Math.ceil(impact.nodes.length / 10));
  const current = Math.min(page, pages - 1);
  const historyPages = Math.max(1, Math.ceil(impact.historical_nodes.length / 10));
  const currentHistory = Math.min(historyPage, historyPages - 1);
  return <div className="space-y-3 rounded-lg border p-3 text-sm">
    <p className="font-medium">影响范围</p>
    <p>{impact.reference_count} 个直接关联资源{impact.restart_count > 0 ? `；${impact.restart_count} 个 sing-box 节点或转发可能受进程重启影响（包含直接关联资源）` : "；本次没有已配置资源的连带重启"}。</p>
    {!impact.runtime_change && <p>本次只变更出口模板，不发布新的业务配置；已有引用继续固定原版本。</p>}
    {!impact.server_enabled && <p className="text-amber-700 dark:text-amber-300">服务器配置已停用；保存本次变更不会自行启用服务器。</p>}
    {impact.restart_scope === "server_singbox" && <p className="text-amber-700 dark:text-amber-300">当前应用流程可能重启该服务器全部 sing-box 权限组，已有连接可能中断。下表依据已保存的配置列出节点，不能据此判断实时运行状态。</p>}
    {impact.before_listen && <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1"><dt>监听地址</dt><dd className="break-all">{impact.before_listen} → {impact.after_listen}</dd><dt>访问地址</dt><dd className="break-all">{impact.before_host || "未配置"} → {impact.after_host || "未配置"}</dd></dl>}
    {impact.nodes.length > 0 && <ul className="divide-y">{impact.nodes.slice(current * 10, current * 10 + 10).map((n) => <li key={n.node_id} className="space-y-1 py-2">
      <div className="flex flex-wrap items-center gap-2"><span className="break-all font-medium">{n.name} · #{n.node_id}</span><Badge variant="outline">{effectLabels[n.effect]}</Badge></div>
      <p className="text-xs text-muted-foreground">{n.protocol} · 监听端口 {n.listen_port} · {n.revoked ? "已撤销" : n.blocked ? "配置要求阻断" : "配置要求启用"}{n.egress_profile_id ? ` · 出口 #${n.egress_profile_id} 固定版本 ${n.egress_revision}` : ""}</p>
      {n.share_id && <p className="break-all text-xs text-muted-foreground">分享：{n.share_name || `#${n.share_id}`}</p>}
    </li>)}</ul>}
    {!!impact.forwards?.length && <div className="max-h-64 space-y-2 overflow-auto border-t pt-3"><p className="font-medium">受影响的固定转发（{impact.forwards.length}）</p>{impact.forwards.map((f) => <div key={f.forward_id}><p>{f.name} · #{f.forward_id} <Badge variant="outline">{effectLabels[f.effect]}</Badge></p><p className="text-xs text-muted-foreground">监听端口 {f.listen_port} · 版本 {f.revision}{f.egress_profile_id ? ` · 出口 #${f.egress_profile_id} 固定版本 ${f.egress_revision}` : ""}{f.retired ? " · 正在清理" : !f.enabled ? " · 已停用" : ""}</p></div>)}</div>}
    {!!impact.historical_forwards?.length && <div className="max-h-48 space-y-1 overflow-auto border-t pt-3"><p className="font-medium">仍可能待清理的旧转发入口</p>{impact.historical_forwards.map((f) => <p key={`${f.forward_id}-${f.revision}-${f.listen_port}`} className="text-xs">转发 #{f.forward_id} · 版本 {f.revision} · 端口 {f.listen_port}</p>)}</div>}
    {pages > 1 && <div className="flex flex-wrap items-center justify-end gap-2"><Button size="sm" variant="outline" disabled={current === 0} onClick={() => setPage(current - 1)}>上一页影响</Button><span className="text-xs">{current + 1} / {pages}</span><Button size="sm" variant="outline" disabled={current + 1 === pages} onClick={() => setPage(current + 1)}>下一页影响</Button></div>}
    {impact.historical_nodes.length > 0 && <div className="space-y-2 border-t pt-3"><p className="font-medium">旧配置中还可能受影响的入口（{impact.historical_nodes.length}）</p><p className="text-xs text-muted-foreground">这些入口来自上次完整应用以来的已发布配置，可能尚未完成清理；不会因列在这里而恢复。</p><ul className="space-y-2">{impact.historical_nodes.slice(currentHistory * 10, currentHistory * 10 + 10).map((n) => <li className="break-all text-xs" key={JSON.stringify([n.node_id, n.listen_port, n.protocol, n.name, n.share_id ?? 0])}>{n.name} · #{n.node_id} · {n.protocol} · 端口 {n.listen_port}{n.share_id ? ` · 分享 #${n.share_id}` : ""}</li>)}</ul>{historyPages > 1 && <div className="flex flex-wrap justify-end gap-2"><Button size="sm" variant="outline" disabled={currentHistory === 0} onClick={() => setHistoryPage(currentHistory - 1)}>上一页旧入口</Button><span>{currentHistory + 1} / {historyPages}</span><Button size="sm" variant="outline" disabled={currentHistory + 1 === historyPages} onClick={() => setHistoryPage(currentHistory + 1)}>下一页旧入口</Button></div>}</div>}
    {!impact.history_complete && <p role="alert" className="text-amber-700 dark:text-amber-300">运行配置历史不完整，无法列全仍可能运行的旧入口。本次应按该服务器全部受管 sing-box 进程可能受影响核对，不能把上面的数量当作完整运行范围。</p>}
    <p className="text-xs text-muted-foreground">本次不切换服务器计费网卡。提交时再次核对依赖；保存和实际应用分别确认。</p>
  </div>;
}
