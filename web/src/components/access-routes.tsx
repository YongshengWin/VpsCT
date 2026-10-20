import { nodeSelectFilters } from "@/lib/node-select-filters";
import * as React from "react";
import { Link, useNavigate } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, get, post } from "@/lib/api";
import type { Node, Server } from "@/lib/types";
import { networkOperationID, networkRequestUncertain, type EgressProfile, type NetworkReadiness } from "@/lib/network";
import { Badge, Button, Card, CardHeader, CardTitle, CardContent, Dialog, Field, Input, Select, Textarea } from "@/components/ui";
import { NetworkChecks } from "@/components/network-operations";
import { NetworkImpactPreview } from "@/components/network-impact";
import { useToast } from "@/components/toast";
import { copyText } from "@/lib/utils";

export type AccessRouteRequest = {
 operation_id: string; name: string; entry_server_id: number; ix_server_id: number; landing_node_id: number;
 landing_protocol?: string; landing_source_host?: string;
 entry_port: number; ix_port: number; advertise_host: string; ix_host: string; landing_host: string; landing_port: number;
 network: "tcp" | "both"; entry_egress_id: number; entry_egress_revision: number; ix_egress_id: number; ix_egress_revision: number;
 source_mode: "all" | "cidr"; source_cidrs: string[]; expected_impact?: string;
};
export type AccessRoute = { landing_node_name?: string; landing_server_name?: string; id: string; name: string; request: AccessRouteRequest; entry_forward_id: number; ix_forward_id: number; subscription_id: number | null; stage: string; message: string; revision: number; entry_status: string; ix_status: string };
type Preview = { ready: boolean; token: string; candidate: AccessRouteRequest; entry: NetworkReadiness; ix: NetworkReadiness; waiting_authorization: boolean };
const stages: Record<string, string> = { pending_landing: "等待落地条件", pending_ix: "配置中转到落地", pending_entry: "配置入口转发", applied: "配置已应用", blocked: "线路暂停", stopping_entry: "清理入口", stopping_ix: "清理中转", retired: "已停止并清理" };
const statuses: Record<string, string> = { offline: "离线", applied: "已应用", waiting: "等待回执", publishing: "等待发布", publish_failed: "发布失败", apply_failed: "应用失败" };
const initial = (server: Server): AccessRouteRequest => ({ operation_id: networkOperationID(), name: "", entry_server_id: server.id, ix_server_id: 0, landing_node_id: 0, entry_port: 0, ix_port: 0, advertise_host: "", ix_host: "", landing_host: "", landing_port: 0, network: "tcp", entry_egress_id: 0, entry_egress_revision: 0, ix_egress_id: 0, ix_egress_revision: 0, source_mode: "all", source_cidrs: [] });

export function AccessRoutes({ server }: { server: Server }) {
 const toast = useToast(), qc = useQueryClient(), navigate = useNavigate();
 const [open, setOpen] = React.useState(false), [stop, setStop] = React.useState<AccessRoute | null>(null);
 const routes = useQuery({ queryKey: ["access-routes", server.id], queryFn: () => get<AccessRoute[]>(`/api/v1/servers/${server.id}/access-routes`), refetchInterval: 5000 });
 const servers = useQuery({ queryKey: ["servers"], queryFn: () => get<Server[]>("/api/v1/servers") });
 const name = (id: number) => servers.data?.find(s => s.id === id)?.name ?? `服务器 #${id}`;
 const refresh = () => { void qc.invalidateQueries({ queryKey: ["access-routes"] }); void qc.invalidateQueries({ queryKey: ["server-forwards"] }); void qc.invalidateQueries({ queryKey: ["subscriptions"] }); };
 const remove = useMutation({ mutationFn: (r: AccessRoute) => api(`/api/v1/access-routes/${r.id}`, { method: "DELETE", json: { expected_revision: r.revision } }), onSuccess: () => { setStop(null); refresh(); toast.success("正在停止线路，按顺序清理线路转发"); }, onError: e => toast.fromError(e) });
 const retry = useMutation({ mutationFn: (r: AccessRoute) => post(`/api/v1/access-routes/${r.id}/retry`, { expected_revision: r.revision }), onSuccess: refresh, onError: e => toast.fromError(e) });
 const copy = useMutation({ mutationFn: async (r: AccessRoute) => { const n = await get<{ uri: string }>(`/api/v1/access-routes/${r.id}/node`); if (!await copyText(n.uri)) throw new Error("复制失败，请允许浏览器访问剪贴板后重试"); }, onSuccess: () => toast.success("已复制线路接入节点"), onError: e => toast.fromError(e) });
 return <Card>
  <CardHeader className="flex-row flex-wrap items-center justify-between gap-3"><div><CardTitle>转发线路</CardTitle><p className="mt-1 text-sm text-muted-foreground">选择入口和落地节点；可按需增加一台中转，自动配置转发并生成订阅。</p></div><Button onClick={() => setOpen(true)}>创建线路</Button></CardHeader>
  <CardContent className="space-y-3">
   {routes.isError && <p role="alert" className="text-sm text-destructive">线路加载失败：{routes.error.message}</p>}
   {routes.isPending && <p className="text-sm text-muted-foreground">正在加载线路…</p>}
   {routes.data?.length === 0 && <div className="rounded-lg border border-dashed p-4 text-sm"><p>入口 → 落地节点 → 网站；也可增加中转服务器</p><p className="mt-2 text-muted-foreground">两台即可使用：第 2 台运行落地节点。需要额外中转时再选择第 3 台；也支持导入的外部落地。机器之间的网络与路由需事先接通。</p></div>}
   {routes.data?.map(r => <div key={r.id} className="space-y-3 rounded-lg border p-4">
    <div className="flex flex-wrap items-center justify-between gap-2"><h3 className="font-medium">{r.name}</h3><Badge variant={r.stage === "applied" ? "outline" : "secondary"}>{stages[r.stage] ?? r.stage}</Badge></div>
    <p className="wrap-break-word text-sm">{name(r.request.entry_server_id)} → {r.request.ix_server_id > 0 && <>{name(r.request.ix_server_id)} → </>} {r.landing_server_name ?? "落地服务器"}（{r.landing_node_name ?? `节点 #${r.request.landing_node_id}`}）</p>
    <p className="text-sm text-muted-foreground">{r.message.replaceAll("国内入口", "入口").replaceAll("国内", "入口").replaceAll("IX", "中转").replaceAll("专线", "线路")}</p>
    <div className="flex flex-wrap items-center gap-2">
     {r.stage !== "retired" && <><Button size="sm" disabled={r.stage !== "applied" || copy.isPending} onClick={() => copy.mutate(r)}>复制节点</Button>{r.subscription_id && <Link to={`/subscriptions/${r.subscription_id}/edit`}><Button variant="outline" size="sm">查看订阅</Button></Link>}{!r.stage.startsWith("stopping_") && <Button variant="outline" size="sm" onClick={() => setStop(r)}>停止并清理</Button>}{r.stage !== "applied" && <Button variant="link" size="sm" disabled={retry.isPending} onClick={() => retry.mutate(r)}>重试配置</Button>}</>}
    </div>
    <details className="text-xs text-muted-foreground"><summary className="cursor-pointer">查看分段状态和连接设置</summary><div className="mt-3 space-y-2">
     <p>入口：{statuses[r.entry_status] ?? "等待"}{r.request.ix_server_id > 0 && <> · 中转：{statuses[r.ix_status] ?? "等待"}</>} · {r.request.network === "both" ? "TCP + UDP" : "仅 TCP"}</p>
     <p className="break-all">客户端连接 {r.request.advertise_host}:{r.request.entry_port}，使用落地节点的协议和认证参数。</p>
     <p>每段独立统计转发用量，不重复扣除落地分享配额。</p>
     <Link className="mr-4 text-primary underline" to={`/servers/${r.request.entry_server_id}?tab=forwards`}>入口到{r.request.ix_server_id ? "中转" : "落地"}：转发 #{r.entry_forward_id}</Link>{r.ix_forward_id > 0 && <Link className="text-primary underline" to={`/servers/${r.request.ix_server_id}?tab=forwards`}>中转到落地：转发 #{r.ix_forward_id}</Link>}
     <p>使用私网目标时，请由对应机器的 root 为上述转发 ID 和固定目标地址、端口配置本机授权，等待 agent 上报后自动继续。没有进行端到端测试时，“已应用”仅代表配置回执确认。</p>
    </div></details>
   </div>)}
  </CardContent>
  {open && <RouteEditor key={server.id} server={server} servers={servers.data ?? []} onClose={() => setOpen(false)} onSaved={r => { setOpen(false); refresh(); if (r.request.entry_server_id !== server.id) navigate(`/servers/${r.request.entry_server_id}?tab=routes`); }} />}
  {stop && <Dialog open title={`停止线路 · ${stop.name}`} onClose={() => { if (!remove.isPending) setStop(null); }} footer={<><Button variant="outline" disabled={remove.isPending} onClick={() => setStop(null)}>取消</Button><Button variant="destructive" loading={remove.isPending} onClick={() => remove.mutate(stop)}>停止并清理</Button></>}><p className="text-sm">关闭入口并结算用量；有额外中转时，再按顺序清理中转段。离线端会等待恢复，清理回执确认前端口继续保留。落地节点和其他线路继续保留；本线路订阅将不再输出接入节点。</p></Dialog>}
 </Card>;
}

function RouteEditor({ server, servers, onClose, onSaved }: { server: Server; servers: Server[]; onClose: () => void; onSaved: (route: AccessRoute) => void }) {
 const toast = useToast();
 const [draft, setDraft] = React.useState(() => initial(server));
 const [withRelay, setWithRelay] = React.useState(true);
 const [cidrs, setCIDRs] = React.useState("");
 const [review, setReview] = React.useState<Preview | null>(null);
 const patch = (value: Partial<AccessRouteRequest>) => setDraft(d => ({ ...d, ...value }));
 const nodes = useQuery({ queryKey: ["route-landing-nodes"], queryFn: () => get<Node[]>("/api/v1/nodes") });
 const entryEgress = useQuery({ queryKey: ["egress-profiles", draft.entry_server_id], enabled: draft.entry_server_id > 0, queryFn: () => get<EgressProfile[]>(`/api/v1/servers/${draft.entry_server_id}/egress-profiles`) });
 const relayEgress = useQuery({ queryKey: ["egress-profiles", draft.ix_server_id], enabled: draft.ix_server_id > 0, queryFn: () => get<EgressProfile[]>(`/api/v1/servers/${draft.ix_server_id}/egress-profiles`) });
 const enabled = servers.filter(s => s.enabled);
 const serverName = (id: number) => servers.find(s => s.id === id)?.name ?? `服务器 #${id}`;
 const selectedNode = nodes.data?.find(n => n.id === draft.landing_node_id);
 const landingServerID = selectedNode?.source === "deployed" ? selectedNode.server_id : undefined;
 const needsUDP = (n: Node) => ["hysteria2", "tuic", "wireguard"].includes(n.protocol) || (n.protocol === "mieru" && n.params.transport === "UDP");
 const tcpOnly = (n?: Node) => !!n && n.protocol === "ss" && (n.params.udp === false || n.params.network === "tcp");
 const landingLabel = (n: Node) => `${n.source === "deployed" ? serverName(n.server_id!) : n.source === "imported" ? "导入节点" : "手动节点"} / ${n.name} · ${n.protocol.toUpperCase()}`;
 const landingNodes = nodes.data?.filter(n => n.enabled && !n.revoked && ["ss", "vmess", "vless", "trojan", "anytls", "snell", "mieru", "hysteria2", "tuic", "wireguard", "socks5", "http"].includes(n.protocol) && (n.source === "deployed" ? n.server_id && enabled.some(s => s.id === n.server_id) && n.server_id !== draft.entry_server_id && n.server_id !== draft.ix_server_id : ["imported", "manual"].includes(n.source) && !n.server_id)) ?? [];
 const suggestedName = draft.entry_server_id && (!withRelay || draft.ix_server_id) && selectedNode ? `${serverName(draft.entry_server_id)} → ${withRelay ? `${serverName(draft.ix_server_id)} → ` : ""}${selectedNode.name}`.slice(0, 100) : "选择后自动命名";
 const preview = useMutation({ mutationFn: () => post<Preview>("/api/v1/access-routes/preview", { ...draft, name: draft.name.trim() || suggestedName, source_cidrs: draft.source_mode === "all" ? [] : cidrs.split(/[\s,]+/).filter(Boolean) }), onSuccess: setReview, onError: e => toast.fromError(e, "线路检查失败") });
 const save = useMutation({ mutationFn: () => post<AccessRoute>("/api/v1/access-routes", { ...review!.candidate, expected_impact: review!.token }), onSuccess: r => { toast.success("线路已创建，正在依次配置；订阅已生成"); onSaved(r); }, onError: e => toast.fromError(e) });
 const busy = preview.isPending || save.isPending;
 const uncertain = networkRequestUncertain(save.error);
 const egressField = (relay: boolean) => {
  const query = relay ? relayEgress : entryEgress;
  const label = relay ? "中转到落地的出站路径" : withRelay ? "入口到中转的出站路径" : "入口到落地的出站路径";
  return <Field label={label} hint="默认使用服务器路由；需要指定网卡时，选择已有直连出口。"><Select aria-label={label} value={relay ? draft.ix_egress_id : draft.entry_egress_id} onChange={e => { const p = query.data?.find(p => p.id === Number(e.target.value)); patch(relay ? { ix_egress_id: p?.id ?? 0, ix_egress_revision: p?.current_revision ?? 0 } : { entry_egress_id: p?.id ?? 0, entry_egress_revision: p?.current_revision ?? 0 }); }}><option value={0}>使用服务器默认路由</option>{query.data?.filter(p => p.enabled && p.kind === "direct").map(p => <option key={p.id} value={p.id}>{p.name}</option>)}</Select>{query.isError && <p role="alert" className="text-sm text-destructive">出站路径加载失败，请重新打开后重试。</p>}</Field>;
 };
 return <Dialog open title="创建线路" size="lg" onClose={() => { if (!busy && !uncertain) onClose(); }} footer={<><Button variant="outline" disabled={busy || uncertain} onClick={() => { if (review) { setReview(null); save.reset(); } else onClose(); }}>{review ? "返回修改" : "取消"}</Button>{review ? <Button loading={save.isPending} disabled={!review.ready} onClick={() => save.mutate()}>{save.isError ? "重试同一请求" : review.waiting_authorization ? "创建并等待私网授权" : "创建线路与订阅"}</Button> : <Button loading={preview.isPending} disabled={!draft.entry_server_id || (withRelay && !draft.ix_server_id) || !landingNodes.some(n => n.id === draft.landing_node_id) || (draft.source_mode === "cidr" && !cidrs.trim())} onClick={() => preview.mutate()}>检查配置</Button>}</>}>
  {review ? <div className="space-y-4 text-sm">
   <h3 className="font-medium">{review.candidate.name}</h3>
   <div className="space-y-2 rounded-lg border p-3">
    <p className="break-all">客户端 → 入口：{review.candidate.advertise_host}:{review.candidate.entry_port}</p>
    {review.candidate.ix_server_id > 0 && <p className="break-all">入口 → 中转：{review.candidate.ix_host}:{review.candidate.ix_port}</p>}
    <p className="break-all">{review.candidate.ix_server_id ? "中转" : "入口"} → 落地：{review.candidate.landing_host}:{review.candidate.landing_port}</p>
    <p>保留落地节点的协议、认证和 TLS / 传输参数 · {review.candidate.network === "both" ? "TCP + UDP" : "仅 TCP"}</p>
   </div>
   <p>{review.candidate.ix_server_id ? "系统先配置中转到落地，再开放入口" : "系统确认落地条件后配置入口直达落地"}，并自动生成接入订阅。{selectedNode?.source !== "deployed" ? "外部落地不由本面板管理，其可用性需客户端验证；上游端点变化后线路会暂停，需重新配置。" : ""}</p>
   {review.waiting_authorization && <p role="status" className="rounded-lg border p-3">私网目标需要本机授权。创建后会显示对应转发 ID；由 root 配置对应授权并等待 agent 上报后自动继续，期间不开放客户端入口。</p>}
   <details open={!review.ready}><summary className="cursor-pointer font-medium">入口 → {review.candidate.ix_server_id ? "中转" : "落地"}：检查结果</summary><NetworkChecks view={review.entry} /></details>
   {review.candidate.ix_server_id > 0 && <details open={!review.ready}><summary className="cursor-pointer font-medium">中转 → 落地：检查结果</summary><NetworkChecks view={review.ix} /></details>}
   {review.entry.impact && <NetworkImpactPreview impact={review.entry.impact} />}{review.candidate.ix_server_id > 0 && review.ix.impact && <NetworkImpactPreview impact={review.ix.impact} />}
   <p className="text-muted-foreground">参与转发的服务器共享代理进程可能重启，影响其中现有连接。配置应用后，还需通过客户端验证连通性。</p>
   {save.isError && <p role="alert" className="text-destructive">{save.error.message}{uncertain ? "。结果尚未确认，请重试同一请求，避免重复创建。" : "。请返回修改并重新检查。"}</p>}
  </div> : <fieldset disabled={busy} className="space-y-4">
   <Field label="线路结构" hint="默认使用三台；也可切换为两台，由第 2 台直接运行落地节点。"><Select aria-label="线路结构" value={withRelay ? "relay" : "direct"} onChange={e => { const relay = e.target.value === "relay"; setWithRelay(relay); if (!relay) patch({ ix_server_id: 0, ix_host: "", ix_port: 0, ix_egress_id: 0, ix_egress_revision: 0 }); }}><option value="relay">三台：入口 → 中转 → 落地节点</option><option value="direct">两台：入口 → 落地节点</option></Select></Field>
   <p className="text-sm text-muted-foreground">客户端 → 入口 → {withRelay ? "中转 → " : ""}落地节点 → 网站。地址和端口自动读取或分配。</p>
   <section className="space-y-3 rounded-lg border p-4">
    <Field label="1. 入口服务器" hint="客户端连接这台服务器。"><Select searchable aria-label="入口服务器" value={draft.entry_server_id} onChange={e => patch({ entry_server_id: Number(e.target.value), entry_port: 0, advertise_host: "", entry_egress_id: 0, entry_egress_revision: 0 })}><option value={0}>选择入口服务器</option>{enabled.filter(s => s.id !== draft.ix_server_id && s.id !== landingServerID).map(s => <option key={s.id} value={s.id}>{s.name}</option>)}</Select></Field>
    <details className="text-sm"><summary className="cursor-pointer text-muted-foreground">入口设置（可选）</summary><div className="mt-3 space-y-4">
     <Field label="客户端接入地址" hint="留空使用入口服务器的访问地址。"><Input aria-label="客户端接入地址" value={draft.advertise_host} placeholder="自动读取" onChange={e => patch({ advertise_host: e.target.value })} /></Field>
     <Field label="入口监听端口" hint="留空或填 0 自动分配可用端口。"><Input aria-label="入口监听端口" type="number" min={0} max={65535} value={draft.entry_port || ""} placeholder="自动分配" onChange={e => patch({ entry_port: Number(e.target.value) })} /></Field>
     <Field label="允许连接入口的客户端"><Select aria-label="客户端来源" value={draft.source_mode} onChange={e => patch({ source_mode: e.target.value as "all" | "cidr" })}><option value="all">任何来源，仍须通过落地节点认证</option><option value="cidr">仅指定地址段</option></Select></Field>
     {draft.source_mode === "cidr" && <Field label="允许来源 CIDR"><Textarea aria-label="来源 CIDR" rows={3} value={cidrs} onChange={e => setCIDRs(e.target.value)} /></Field>}
    </div></details>
   </section>
   {withRelay && <>
   <section className="space-y-3 rounded-lg border p-4">
    <Field label="2. 中转服务器" hint="入口把连接转发到这里，再送往落地节点。"><Select searchable aria-label="中转服务器" value={draft.ix_server_id} onChange={e => patch({ ix_server_id: Number(e.target.value), ix_port: 0, ix_host: "", ix_egress_id: 0, ix_egress_revision: 0 })}><option value={0}>选择中转服务器</option>{enabled.filter(s => s.id !== draft.entry_server_id && s.id !== landingServerID).map(s => <option key={s.id} value={s.id}>{s.name}</option>)}</Select></Field>
    <details className="text-sm"><summary className="cursor-pointer text-muted-foreground">入口 → 中转设置（可选）</summary><div className="mt-3 space-y-4">
     <Field label="中转连接地址" hint="入口使用这个地址连接中转。留空使用中转服务器访问地址；走专线或内网时填该段地址。"><Input aria-label="中转连接地址" value={draft.ix_host} placeholder="自动读取，可填写专线或内网地址" onChange={e => patch({ ix_host: e.target.value })} /></Field>
     <Field label="中转监听端口" hint="留空或填 0 自动分配；可与入口端口不同。"><Input aria-label="中转监听端口" type="number" min={0} max={65535} value={draft.ix_port || ""} placeholder="自动分配" onChange={e => patch({ ix_port: Number(e.target.value) })} /></Field>
     {egressField(false)}
    </div></details>
   </section>
   </>}
   <section className="space-y-3 rounded-lg border p-4">
    <Field label={withRelay ? "3. 落地节点" : "2. 落地节点"} hint="支持受管部署、导入和手动节点。自动读取协议、端口和认证；入口和中转无需部署代理节点。"><Select searchable quickFilters={nodeSelectFilters(landingNodes, servers)} searchPlaceholder="搜索名称、服务器、协议或来源…" aria-label="落地节点" value={draft.landing_node_id} onChange={e => { const n = nodes.data?.find(n => n.id === Number(e.target.value)); patch({ landing_node_id: Number(e.target.value), landing_port: 0, landing_host: "", network: n && needsUDP(n) ? "both" : "tcp" }); }}><option value={0}>{nodes.isPending ? "正在加载节点…" : "选择落地节点"}</option>{landingNodes.map(n => <option key={n.id} value={n.id}>{landingLabel(n)}</option>)}</Select>
     {nodes.isError && <p role="alert" className="text-sm text-destructive">节点加载失败：{nodes.error.message}<button type="button" className="ml-2 underline" onClick={() => void nodes.refetch()}>重试</button></p>}
     {nodes.isSuccess && !landingNodes.length && <p className="text-sm text-muted-foreground">没有可用落地节点。请先导入节点，或在另一台服务器部署代理节点。</p>}
     <Link className="text-xs text-primary underline" to={landingServerID ? `/servers/${landingServerID}?tab=nodes` : "/servers"}>管理或部署节点</Link><Link className="ml-3 text-xs text-primary underline" to="/externals">导入节点</Link>
    </Field>
    {selectedNode && <p className="text-xs text-muted-foreground">{landingLabel(selectedNode)} · 节点端口 {selectedNode.port}{selectedNode.source !== "deployed" ? " · 外部落地没有应用回执，需客户端验证" : ""}{needsUDP(selectedNode) ? " · 该协议使用 UDP 传输，已启用 TCP + UDP 转发" : ""}</p>}
    <details className="text-sm"><summary className="cursor-pointer text-muted-foreground">{withRelay ? "中转" : "入口"} → 落地设置（可选）</summary><div className="mt-3 space-y-4">
     <Field label="落地连接地址" hint={`${withRelay ? "中转" : "入口"}使用这个地址连接落地。默认读取所选节点地址；走专线或内网时填写该段地址。域名目标需带 DNS 的出站路径，端口跟随所选节点。`}><Input aria-label="落地连接地址" value={draft.landing_host} placeholder="自动读取，可填写专线或内网地址" onChange={e => patch({ landing_host: e.target.value })} /></Field>
     {egressField(withRelay)}
    </div></details>
   </section>
   <details className="rounded-lg border p-3 text-sm"><summary className="cursor-pointer text-muted-foreground">线路名称与连接类型（可选）</summary><div className="mt-3 space-y-4">
    <Field label="线路名称" hint="留空按所选线路自动命名。"><Input aria-label="线路名称" value={draft.name} maxLength={100} placeholder={suggestedName} onChange={e => patch({ name: e.target.value })} /></Field>
    <Field label="连接类型"><Select aria-label="连接类型" value={draft.network} onChange={e => patch({ network: e.target.value as AccessRouteRequest["network"] })}><option value="tcp" disabled={!!selectedNode && needsUDP(selectedNode)}>仅 TCP（默认；支持 VLESS 等在 TCP 内承载 UDP）</option><option value="both" disabled={tcpOnly(selectedNode)}>TCP + UDP</option></Select></Field>
   </div></details>
   <p className="text-xs text-muted-foreground">使用专线时，需事先接通机器之间的网络和路由。私网 TCP 需对应服务器的本机授权，私网 UDP 转发暂不支持。</p>
  </fieldset>}
 </Dialog>;
}
