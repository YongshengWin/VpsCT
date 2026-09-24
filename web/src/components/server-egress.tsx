import * as React from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, get, post } from "@/lib/api";
import type { NetworkView, Server } from "@/lib/types";
import { egressSummary, networkOperationID, type EgressProfile, type EgressPreview, type EgressView, type NetworkOperation } from "@/lib/network";
import { Badge, Button, Card, CardContent, CardHeader, CardTitle, Dialog, Empty, Spinner } from "@/components/ui";
import { NetworkOperationReceipt, NetworkOperations } from "@/components/network-operations";
import { useToast } from "@/components/toast";
import { NetworkImpactPreview } from "@/components/network-impact";
import { EgressEditor } from "@/components/egress-editor";

type EgressDelete = { operation_id: string; expected_revision: number; id: number; name: string; result?: EgressPreview };

export function ServerEgress({ server }: { server: Server }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [selected, setSelected] = React.useState<number | null>(null);
  const [page, setPage] = React.useState(0);
  const [edit, setEdit] = React.useState<{ initial: EgressView | null } | null>(null);
  const [remove, setRemove] = React.useState<EgressDelete | null>(null);
  const [receipt, setReceipt] = React.useState("");
  const profiles = useQuery({ queryKey: ["egress-profiles", server.id], queryFn: () => get<EgressProfile[]>(`/api/v1/servers/${server.id}/egress-profiles`), refetchInterval: 10000 });
  const inventory = useQuery({ queryKey: ["server-network", server.id], queryFn: () => get<NetworkView>(`/api/v1/servers/${server.id}/network`), refetchInterval: 10000 });
  const detail = useQuery({ queryKey: ["egress-profile", selected, page], enabled: selected !== null, queryFn: () => get<EgressView>(`/api/v1/egress-profiles/${selected}?limit=10&offset=${page * 10}`), refetchInterval: 10000 });
  const saved = (op: NetworkOperation) => {
    setReceipt(op.id); setEdit(null); setRemove(null);
    void qc.invalidateQueries({ queryKey: ["egress-profiles", server.id] }); void qc.invalidateQueries({ queryKey: ["egress-profile"] }); void qc.invalidateQueries({ queryKey: ["network-operations", server.id] }); void qc.invalidateQueries({ queryKey: ["servers"] });
    if (op.kind === "egress_delete") setSelected(null); else { setSelected(op.resource_id); setPage(0); }
    toast.success(op.kind === "egress_delete" ? "出口已删除" : op.status === "saved" ? "出口版本已保存" : "变更已提交，等待服务器应用");
  };
  const previewDelete = useMutation({ mutationFn: (request: EgressDelete) => post<EgressPreview>(`/api/v1/egress-profiles/${request.id}/preview`, { action: "delete", expected_revision: request.expected_revision }), onSuccess: (result, request) => setRemove({ ...request, result }), onError: (e) => toast.fromError(e, "无法预览删除影响") });
  const deleteM = useMutation({ mutationFn: () => api<NetworkOperation>(`/api/v1/egress-profiles/${remove!.id}`, { method: "DELETE", json: { operation_id: remove!.operation_id, expected_revision: remove!.expected_revision, expected_impact: remove!.result!.impact.token } }), onSuccess: saved, onError: (e) => { toast.fromError(e); void qc.invalidateQueries({ queryKey: ["egress-profile"] }); } });
  return <div className="mt-4 space-y-4">
    <Card><CardHeader className="flex-row items-center justify-between gap-3"><CardTitle>已有代理出口</CardTitle><Button onClick={() => setEdit({ initial: null })}>添加出口</Button></CardHeader><CardContent className="space-y-4">
      <p className="text-sm text-muted-foreground">只有你已经有 SOCKS5、SSH 等代理服务时才需要这里。添加后，还要在节点详情中选择它。</p>
      {inventory.isError && <p className="text-sm text-amber-700">网卡清单加载失败，可查看已有配置；新地址选择需等待清单恢复。</p>}
      {profiles.isPending ? <Spinner /> : profiles.isError ? <p className="text-destructive">出口加载失败：{profiles.error.message}</p> : !profiles.data.length ? <Empty title="还没有接入其他代理" description="如果只是让节点走另一台受管 VPS，请返回节点线路添加中转。" /> : <div className="grid gap-2 sm:grid-cols-2">{profiles.data.map((p) => <button key={p.id} aria-pressed={selected === p.id} className={`rounded-lg border p-3 text-left ${selected === p.id ? "border-primary bg-primary/5" : "hover:bg-muted/40"}`} onClick={() => { setSelected(p.id); setPage(0); }}><div className="flex items-center justify-between gap-2"><span className="break-words font-medium">{p.name}</span><Badge variant={p.enabled ? "outline" : "secondary"}>{p.enabled ? "配置：启用" : "配置：停用"}</Badge></div><p className="mt-1 text-xs text-muted-foreground">最新版本 {p.current_revision} · {p.kind === "wireguard" ? "WireGuard 中转" : p.kind === "ssh" ? "SSH 中转" : p.kind === "ss2022" ? "SS-2022 中转" : p.kind === "socks5" ? "SOCKS5 中转" : "直连"}</p></button>)}</div>}
      {selected !== null && (detail.isPending ? <Spinner /> : detail.isError ? <p role="alert" className="text-destructive">出口详情加载失败：{detail.error.message}</p> : <div className="space-y-3 rounded-lg border p-4">
        <div className="flex flex-wrap items-center justify-between gap-2"><h3 className="font-medium">{detail.data.profile.name} · 最新版本 {detail.data.profile.current_revision}</h3><div className="flex gap-2">{detail.data.profile.managed_stage ? <Badge>由托管中转管理</Badge> : <Button size="sm" variant="outline" onClick={() => setEdit({ initial: detail.data })}>编辑出口</Button>}<Button size="sm" variant="outline" disabled={Boolean(detail.data.profile.managed_stage && detail.data.profile.managed_stage !== "retired") || detail.data.reference_count > 0 || (detail.data.forward_reference_count ?? 0) > 0} onClick={() => { deleteM.reset(); const request = { operation_id: networkOperationID(), expected_revision: detail.data.profile.current_revision, id: selected, name: detail.data.profile.name }; setRemove(request); previewDelete.mutate(request); }}>删除</Button></div></div>
        <p className="break-all text-sm">{egressSummary(detail.data.revision.config, inventory.data)}</p>
        {detail.data.profile.kind !== "direct" && <p className="text-xs text-muted-foreground">上游认证：{detail.data.revision.has_credentials ? "已配置凭据（不显示原值）" : "无认证"}</p>}
        <p className="text-xs text-muted-foreground">以下 {detail.data.reference_count} 个节点、{detail.data.forward_reference_count ?? 0} 个转发引用该出口；括号内是各自固定版本。暂停节点也保留引用，解除所有引用后才能删除。</p>
        <ul className="space-y-1 text-sm">{detail.data.forward_references?.map((f) => <li key={`forward-${f.forward_id}`}>{f.name} · 转发 #{f.forward_id}（固定出口版本 {f.egress_revision}）{f.retired ? " · 正在清理" : !f.enabled ? " · 已停用" : ""}</li>)}{detail.data.references.map((n) => <li key={n.node_id}>{n.name} · 节点 #{n.node_id}（版本 {n.revision}）{!n.enabled && " · 节点已禁用"}</li>)}</ul>
        {detail.data.reference_count > 10 && <div className="flex items-center justify-end gap-2"><Button size="sm" variant="outline" disabled={page === 0 || detail.isFetching} onClick={() => setPage(page - 1)}>上一页引用</Button><Button size="sm" variant="outline" disabled={(page + 1) * 10 >= detail.data.reference_count || detail.isFetching} onClick={() => setPage(page + 1)}>下一页引用</Button></div>}
      </div>)}
    </CardContent></Card>
    {receipt && <NetworkOperationReceipt key={receipt} id={receipt} />}
    <details className="rounded-xl border bg-card p-4"><summary className="cursor-pointer font-medium">查看网络变更记录</summary><div className="mt-4"><NetworkOperations serverID={server.id} /></div></details>
    {edit && <EgressEditor serverID={server.id} initial={edit.initial} currentRevision={detail.data?.profile.id === edit.initial?.profile.id ? detail.data?.profile.current_revision : undefined} inventory={inventory.data} onClose={() => setEdit(null)} onSaved={saved} />}
    {remove && <Dialog open title={`删除出口 · ${remove.name}`} onClose={() => { if (!deleteM.isPending && !previewDelete.isPending) setRemove(null); }} footer={<><Button variant="outline" disabled={deleteM.isPending || previewDelete.isPending} onClick={() => setRemove(null)}>关闭</Button><Button variant="destructive" loading={deleteM.isPending} disabled={!remove.result?.ready || previewDelete.isPending} onClick={() => deleteM.mutate()}>{deleteM.isError ? "重试同一删除请求" : "删除未引用出口"}</Button></>}><div className="space-y-3 text-sm"><p>将删除此出口及其历史版本。提交时会再次核对版本和引用；有节点或转发引用时不会删除。</p>{previewDelete.isPending ? <Spinner /> : previewDelete.isError ? <p role="alert" className="text-destructive">{previewDelete.error.message}</p> : remove.result && <><NetworkImpactPreview impact={remove.result.impact} />{remove.result.issues.map((issue, i) => <p key={i} role="alert" className="text-destructive">{issue}</p>)}</>}{deleteM.isError && <p role="alert" className="text-destructive">{deleteM.error.message}。请核对依赖或版本后再操作。</p>}</div></Dialog>}
  </div>;
}
