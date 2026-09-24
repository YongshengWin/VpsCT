import * as React from "react";
import { Link } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { get, post, put } from "@/lib/api";
import type { NetworkView, Node } from "@/lib/types";
import { canBindNodeNetwork, egressSummary, networkOperationID, networkRequestUncertain, type EgressProfile, type EgressRevision, type NetworkOperation, type NetworkReadiness, type NodeNetworkPolicy } from "@/lib/network";
import { Badge, Button, Field, Input, Select, Spinner } from "@/components/ui";
import { NetworkAddressSelect } from "@/components/network-address";
import { NetworkChecks, NetworkOperationReceipt } from "@/components/network-operations";
import { useToast } from "@/components/toast";
import { NetworkImpactPreview } from "@/components/network-impact";

type BindingRequest = { operation_id: string; expected_revision: number; network: NodeNetworkPolicy | null; advertise_host?: string; expected_impact?: string };

function NodeNetworkEditor({ node, currentRevision, inventory, profiles, onClose, onSaved }: { node: Node; currentRevision: number; inventory?: NetworkView; profiles: EgressProfile[]; onClose: () => void; onSaved: (op: NetworkOperation) => void }) {
  const [mode, setMode] = React.useState<"binding" | "reset">("binding");
  const [policy, setPolicy] = React.useState<NodeNetworkPolicy>(node.network ?? { listen_mode: "all", advertise_mode: "inherit", on_unavailable: "block" });
  const [host, setHost] = React.useState(node.server);
  const [revision, setRevision] = React.useState(String(node.network?.egress_revision ?? ""));
  const [review, setReview] = React.useState<{ request: BindingRequest; result: NetworkReadiness } | null>(null);
  const toast = useToast();
  const selected = profiles.find((p) => p.id === policy.egress_profile_id);
  const historical = useQuery({ queryKey: ["egress-revision", policy.egress_profile_id, Number(revision)], enabled: mode === "binding" && !!policy.egress_profile_id && Number.isSafeInteger(Number(revision)) && Number(revision) > 0, queryFn: () => get<EgressRevision>(`/api/v1/egress-profiles/${policy.egress_profile_id}/revisions/${Number(revision)}`) });
  const stale = currentRevision !== (node.network_revision ?? 0);
  const request = (): BindingRequest => ({ operation_id: networkOperationID(), expected_revision: node.network_revision ?? 0, network: mode === "reset" ? null : { ...policy, ...(policy.egress_profile_id ? { egress_revision: Number(revision) } : {}) }, ...(mode === "binding" && policy.advertise_mode === "override" ? { advertise_host: host.trim() } : {}) });
  const preview = useMutation({ mutationFn: async () => {
    const candidate = request();
    const result = await post<NetworkReadiness>(`/api/v1/nodes/${node.id}/network/preview`, { network: candidate.network, ...(candidate.advertise_host !== undefined ? { advertise_host: candidate.advertise_host } : {}) });
    if (!result.impact) throw new Error("控制端未返回影响范围，请更新控制端后重新预览");
    return { request: { ...candidate, expected_impact: result.impact.token }, result };
  }, onSuccess: setReview, onError: (e) => toast.fromError(e, "无法预览网络变更") });
  const save = useMutation({ mutationFn: () => put<NetworkOperation>(`/api/v1/nodes/${node.id}/network`, review!.request), onSuccess: onSaved, onError: (e) => toast.fromError(e) });
  const fieldsValid = mode === "reset" || ((policy.listen_mode === "all" || !!policy.listen_address) && (policy.advertise_mode === "inherit" || !!host.trim()) && (!policy.egress_profile_id || (Number.isSafeInteger(Number(revision)) && Number(revision) >= 1 && !!selected && Number(revision) <= selected.current_revision && historical.isSuccess)));
  const editing = !review;
  const busy = preview.isPending || save.isPending;
  return <div className="space-y-4 rounded-lg border p-4">
    <p className="text-sm font-medium">{editing ? "编辑节点网络" : "核对节点网络变更"}</p>
    {stale && <p role="alert" className="text-sm text-destructive">节点网络版本已变化。{networkRequestUncertain(save.error) ? "上次请求结果尚未确认，请重试同一请求核对结果。" : "请关闭编辑，读取最新版本后重新核对。"}</p>}
    {editing ? <fieldset disabled={busy} className="space-y-4">
      <Field label="网络模式"><Select aria-label="节点网络模式" value={mode} onChange={(e) => setMode(e.target.value as "binding" | "reset")}><option value="binding">配置监听与出口</option><option value="reset">清除绑定，恢复默认网络</option></Select></Field>
      {mode === "reset" ? <p className="text-sm text-amber-700 dark:text-amber-300">申请撤销节点的监听和出口限制，恢复服务器默认网络与继承访问地址。必须等待服务器确认清理，清空表单不代表已经恢复。</p> : <>
        <Field label="节点监听"><Select aria-label="节点监听" value={policy.listen_mode} onChange={(e) => setPolicy({ ...policy, listen_mode: e.target.value as "all" | "address", listen_address: undefined, listen_interface_id: undefined })}><option value="all">全部本机地址</option><option value="address">指定本机 IP</option></Select></Field>
        {policy.listen_mode === "address" && <Field label="监听 IP" hint="只使用 agent 报告的有效本机地址，公网访问地址在下方单独设置。"><NetworkAddressSelect label="监听 IP" optional={false} inventory={inventory} value={policy.listen_address ? { interface_id: policy.listen_interface_id!, address: policy.listen_address } : undefined} onChange={(v) => setPolicy({ ...policy, listen_address: v?.address, listen_interface_id: v?.interface_id })} /></Field>}
        <Field label="业务出口"><Select aria-label="节点业务出口" value={policy.egress_profile_id ?? ""} onChange={(e) => {
          const p = profiles.find((p) => p.id === Number(e.target.value));
          setPolicy({ ...policy, egress_profile_id: p?.id, egress_revision: p?.current_revision }); setRevision(String(p?.current_revision ?? ""));
        }}><option value="">保持默认出口</option>{policy.egress_profile_id && !selected && <option value={policy.egress_profile_id}>原出口 #{policy.egress_profile_id}（无法读取）</option>}{profiles.map((p) => <option key={p.id} value={p.id} disabled={node.core !== "singbox" || !p.enabled || (node.protocol !== "ss" && p.kind !== "direct") || Boolean(p.managed_stage)}>{p.name}{p.enabled ? "" : "（已停用）"}</option>)}</Select></Field>
        {policy.egress_profile_id ? <div className="space-y-2"><Field label="固定出口版本" hint="新模板版本不会自动替换旧绑定。这里明确选择需要应用的版本。"><Input aria-label="固定出口版本" type="number" min={1} max={selected?.current_revision ?? 4096} value={revision} onChange={(e) => setRevision(e.target.value)} /></Field>{selected && <p className="text-xs text-muted-foreground">最新版本 {selected.current_revision}{!selected.enabled && " · 此出口已停用，需到服务器出口页恢复"}</p>}{historical.isFetching ? <Spinner /> : historical.isError ? <p className="text-sm text-destructive">所选版本读取失败：{historical.error.message}</p> : historical.data && <p className="text-xs text-muted-foreground">{egressSummary(historical.data.config, inventory)}</p>}</div> : null}
        <Field label="客户端访问地址"><Select aria-label="客户端访问地址模式" value={policy.advertise_mode} onChange={(e) => setPolicy({ ...policy, advertise_mode: e.target.value as "inherit" | "override" })}><option value="inherit">继承服务器访问地址</option><option value="override">此节点独立设置</option></Select></Field>
        {policy.advertise_mode === "override" && <Field label="独立访问 IP / 域名" hint="填写客户端可达的入口，不能包含协议、端口或路径；不会把出口地址写入订阅。"><Input aria-label="独立访问 IP 或域名" value={host} maxLength={253} onChange={(e) => setHost(e.target.value)} /></Field>}
        {(selected?.kind === "socks5" || selected?.kind === "ssh") && <p className="text-xs text-muted-foreground">私网中转的本机授权须对应节点 #{node.id}、出口 #{selected.id}。上游连接与上游 DNS 分别授权；保存出口模板不会授予本机权限，也不会开放业务访问内网。</p>}
        <p className="text-xs text-muted-foreground">指定的网卡或源地址失效时阻断本节点，不自动改走其他出口。计费来源不随此设置改变。</p>
      </>}
    </fieldset> : <div className="space-y-3 text-sm">
      <NetworkChecks view={review.result} />
      {review.result.impact && <NetworkImpactPreview impact={review.result.impact} />}
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2"><dt>节点</dt><dd>{node.name} · #{node.id}</dd><dt>监听</dt><dd>{review.request.network ? review.request.network.listen_address || "全部本机地址" : "恢复默认监听"}</dd><dt>出口</dt><dd>{review.request.network?.egress_profile_id ? `${selected?.name ?? "原出口"} · 固定版本 ${review.request.network.egress_revision}` : "宿主机默认出口"}</dd><dt>访问地址</dt><dd className="break-all">{review.request.advertise_host ?? "继承服务器访问地址"}</dd></dl>
      <p>应用可能重启同一共享代理进程，影响其中的现有连接。配置服务器网络不会改变节点凭据；访问地址改变后，客户端需要更新订阅。</p>
      {mode === "reset" && <p className="text-amber-700 dark:text-amber-300">确认撤销当前网络绑定并恢复默认路径，等待服务器完成清理。</p>}
      {save.isError && <p role="alert" className="text-destructive">{save.error.message}。{networkRequestUncertain(save.error) ? "请求结果尚未确认；请重试同一请求核对结果。" : "请返回编辑，重新预览并核对后再提交。"}</p>}
    </div>}
    <div className="flex flex-wrap justify-end gap-2"><Button size="sm" variant="outline" disabled={busy} onClick={onClose}>关闭编辑</Button>{review ? <><Button size="sm" variant="outline" disabled={busy || networkRequestUncertain(save.error)} onClick={() => { setReview(null); save.reset(); }}>返回编辑</Button><Button size="sm" disabled={!review.result.ready || (stale && !networkRequestUncertain(save.error))} loading={save.isPending} onClick={() => save.mutate()}>{save.isError ? "重试同一请求" : "提交网络变更"}</Button></> : <Button size="sm" disabled={!fieldsValid || stale} loading={preview.isPending} onClick={() => preview.mutate()}>预览变更</Button>}</div>
  </div>;
}

export function NodeNetwork({ nodeID, serverID, onNavigate }: { nodeID: number; serverID: number; onNavigate: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [editing, setEditing] = React.useState<Node | null>(null);
  const [receipt, setReceipt] = React.useState("");
  const q = useQuery({ queryKey: ["nodes", nodeID, "detail"], queryFn: () => get<Node>(`/api/v1/nodes/${nodeID}`), refetchInterval: 10000 });
  const inventory = useQuery({ queryKey: ["server-network", serverID], queryFn: () => get<NetworkView>(`/api/v1/servers/${serverID}/network`), refetchInterval: 10000 });
  const profiles = useQuery({ queryKey: ["egress-profiles", serverID], queryFn: () => get<EgressProfile[]>(`/api/v1/servers/${serverID}/egress-profiles`), refetchInterval: 10000 });
  const capabilities = useQuery({ queryKey: ["network-capabilities", serverID, q.data?.core], enabled: q.isSuccess, queryFn: () => get<NetworkReadiness>(`/api/v1/servers/${serverID}/network/capabilities?core=${q.data!.core}`), refetchInterval: 10000 });
  const saved = (op: NetworkOperation) => {
    setEditing(null); setReceipt(op.id);
    void qc.invalidateQueries({ queryKey: ["nodes"] }); void qc.invalidateQueries({ queryKey: ["egress-profile"] }); void qc.invalidateQueries({ queryKey: ["network-operations", serverID] }); void qc.invalidateQueries({ queryKey: ["servers"] });
    toast.success("节点网络变更已提交，等待服务器应用");
  };
  if (q.isPending) return <Spinner />;
  if (q.isError) return <p className="mt-4 text-sm text-destructive">节点网络加载失败：{q.error.message}</p>;
  const n = q.data;
  const p = profiles.data?.find((p) => p.id === n.network?.egress_profile_id);
  const qualified = canBindNodeNetwork(n);
  return <section className="mt-6 space-y-3 border-t pt-4">
    <div className="flex flex-wrap items-center justify-between gap-2"><h3 className="font-semibold">服务器网络</h3><Link className="text-sm text-primary underline" to={`/servers/${serverID}?tab=egress`} onClick={onNavigate}>管理服务器出口</Link></div>
    <div className="space-y-2 text-sm"><div className="flex flex-wrap gap-2"><Badge variant="outline">已保存的网络版本 {n.network_revision ?? 0}</Badge><Badge variant="secondary">{n.network ? "指定网络" : "默认网络"}</Badge></div><p>监听：{n.network?.listen_address || "默认 / 全部本机地址"}</p><p>出口：{n.network?.egress_profile_id ? `${p?.name ?? `出口 #${n.network.egress_profile_id}`} · 固定版本 ${n.network.egress_revision}${p && !p.enabled ? "（出口配置已停用）" : ""}` : "宿主机默认出口"}</p><p className="text-xs text-muted-foreground">这里展示已保存的选择，运行状态请核对下方回执或服务器出口页的变更记录。{n.core !== "singbox" && " Snell/mieru 可指定监听 IP，业务出口沿用宿主机路由。"}</p></div>
    {!editing && capabilities.data && !capabilities.data.ready && <NetworkChecks view={capabilities.data} />}
    {(inventory.isError || profiles.isError || capabilities.isError) && <p role="alert" className="text-sm text-destructive">网卡、出口或运行条件暂时无法读取，请刷新后再编辑。</p>}
    {!qualified && <p className="text-xs text-muted-foreground">该协议组合尚未开放新绑定；已有配置可申请清理。直连绑定已支持 SS-2022 AES-128、VLESS Reality、Trojan、AnyTLS、Hysteria2 和 TUIC；中转出口仍限 SS-2022。</p>}
    {!editing && <Button size="sm" variant="outline" disabled={Boolean(p?.managed_stage && p.managed_stage !== "retired") || n.revoked || (!qualified && !n.network) || !profiles.isSuccess || !capabilities.isSuccess} onClick={() => setEditing(n)}>编辑监听与出口</Button>}
    {editing && <NodeNetworkEditor node={editing} currentRevision={n.network_revision ?? 0} inventory={inventory.data} profiles={profiles.data ?? []} onClose={() => setEditing(null)} onSaved={saved} />}
    {receipt && <NetworkOperationReceipt id={receipt} />}
  </section>;
}
