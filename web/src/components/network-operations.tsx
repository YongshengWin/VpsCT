import * as React from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { get, post } from "@/lib/api";
import { networkStatus, networkTerminal, type NetworkOperation, type NetworkReadiness } from "@/lib/network";
import { fmtAgo } from "@/lib/utils";
import { Badge, Button, Card, CardContent, CardHeader, CardTitle, Empty, Spinner } from "@/components/ui";
import { useToast } from "@/components/toast";

export function NetworkChecks({ view }: { view: NetworkReadiness }) {
  return <div className="space-y-2 rounded-lg border p-3 text-sm" role="status">
    <p className="font-medium">{view.ready ? "当前条件检查通过" : "当前条件尚不满足"}</p>
    {!view.ready && <ul className="list-disc space-y-1 pl-5 text-amber-700 dark:text-amber-300">{view.checks.filter((c) => !c.ready).map((c) => <li key={c.code}>{c.message}</li>)}</ul>}
    <p className="text-xs text-muted-foreground">架构：{view.architecture || "未知"} · 锁定内核：{view.pinned_core_version || "未知"}。提交时会再次检查；最终以服务器应用回执为准。</p>
  </div>;
}

export function NetworkOperationItem({ op }: { op: NetworkOperation }) {
  const qc = useQueryClient();
  const toast = useToast();
  const retry = useMutation({ mutationFn: () => post<NetworkOperation>(`/api/v1/network/operations/${op.id}/retry`, { expected_retry_revision: op.retry_revision }), onSuccess: (value) => {
    qc.setQueryData(["network-operation", op.id], value);
    void qc.invalidateQueries({ queryKey: ["network-operations", op.server_id] });
    toast.success("重试已保存，等待新的应用回执");
  }, onError: (e) => { toast.fromError(e); void qc.invalidateQueries({ queryKey: ["network-operations", op.server_id] }); } });
  const failed = op.status === "publish_failed" || op.status === "apply_failed";
  const label = { node_network: "节点网络", egress_create: "创建出口", egress_update: "更新出口", egress_delete: "删除出口", forward_create: "创建转发", forward_update: "更新转发", forward_delete: "删除转发" }[op.kind];
  return <div className="space-y-2 rounded-lg border p-3 text-sm">
    <div className="flex flex-wrap items-center justify-between gap-2"><span>{label} #{op.resource_id} · 版本 {op.resource_revision}</span><Badge variant={failed ? "destructive" : op.status === "applied" ? "success" : networkTerminal(op) ? "secondary" : "warning"}>{networkStatus[op.status]}</Badge></div>
    <p className="break-words text-muted-foreground">{op.message}</p>
    <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground"><span>{fmtAgo(op.updated_at)}{op.desired_revision > 0 ? ` · 配置版本 ${op.desired_revision}` : ""}</span>{(failed || op.status === "waiting_agent") && <Button size="sm" variant="outline" loading={retry.isPending} onClick={() => retry.mutate()}>重新申请应用</Button>}</div>
    {op.status === "applied" && <p className="text-xs text-muted-foreground">已收到对应配置的应用回执；这不代表所有业务目标均可达。</p>}
  </div>;
}

export function NetworkOperationReceipt({ id }: { id: string }) {
  const q = useQuery({ queryKey: ["network-operation", id], queryFn: () => get<NetworkOperation>(`/api/v1/network/operations/${id}`), refetchInterval: (query) => networkTerminal(query.state.data) ? false : 3000 });
  if (q.isPending) return <Spinner />;
  if (q.isError) return <p className="text-sm text-destructive" role="alert">操作状态暂不可读取：{q.error.message}。请到服务器的出口页核对，勿将查询失败当作操作未执行。</p>;
  return <NetworkOperationItem op={q.data} />;
}

export function NetworkOperations({ serverID }: { serverID: number }) {
  const [page, setPage] = React.useState(0);
  const q = useQuery({ queryKey: ["network-operations", serverID, page], queryFn: () => get<NetworkOperation[]>(`/api/v1/servers/${serverID}/network/operations?limit=10&offset=${page * 10}`), refetchInterval: 5000 });
  return <Card><CardHeader><CardTitle>网络变更记录</CardTitle></CardHeader><CardContent className="space-y-3">
    <p className="text-xs text-muted-foreground">这里记录网络配置的应用结果。提交后请等待 VPS 回报状态。</p>
    {q.isPending ? <Spinner /> : q.isError ? <p className="text-sm text-destructive">记录加载失败：{q.error.message}</p> : !q.data.length ? <Empty title="暂无变更记录" /> : q.data.map((op) => <NetworkOperationItem key={op.id} op={op} />)}
    <div className="flex items-center justify-end gap-2"><Button size="sm" variant="outline" disabled={page === 0 || q.isFetching} onClick={() => setPage(page - 1)}>上一页</Button><span className="text-xs">第 {page + 1} 页</span><Button size="sm" variant="outline" disabled={q.isFetching || q.data?.length !== 10} onClick={() => setPage(page + 1)}>下一页</Button></div>
  </CardContent></Card>;
}
