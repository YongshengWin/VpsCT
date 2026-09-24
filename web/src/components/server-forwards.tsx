import * as React from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, get, post } from "@/lib/api";
import type { NetworkView, Server } from "@/lib/types";
import { networkOperationID, networkRequestUncertain, type EgressProfile, type ForwardConfig, type NetworkOperation, type NetworkReadiness, type PortForward } from "@/lib/network";
import { Badge, Button, Card, CardContent, CardHeader, CardTitle, Dialog, Empty, Field, Input, Select, Spinner, Switch, Textarea } from "@/components/ui";
import { NetworkAddressSelect } from "@/components/network-address";
import { NetworkChecks, NetworkOperationReceipt, NetworkOperations } from "@/components/network-operations";
import { NetworkImpactPreview } from "@/components/network-impact";
import { fmtBytes } from "@/lib/utils";
import { useToast } from "@/components/toast";

type Draft = { initial: PortForward | null; deleting?: boolean };
type ForwardWrite = { expected_revision: number; operation_id: string; expected_impact?: string; name?: string; enabled?: boolean; config?: ForwardConfig };
const endpoint = (host: string, port: number) => `${host.includes(":") ? `[${host}]` : host}:${port}`;

export function ServerForwards({ server }: { server: Server }) {
  const qc = useQueryClient();
  const [draft, setDraft] = React.useState<Draft | null>(null);
  const [receipt, setReceipt] = React.useState("");
  const forwards = useQuery({ queryKey: ["server-forwards", server.id], queryFn: () => get<PortForward[]>(`/api/v1/servers/${server.id}/forwards`), refetchInterval: 5000 });
  const inventory = useQuery({ queryKey: ["server-network", server.id], queryFn: () => get<NetworkView>(`/api/v1/servers/${server.id}/network`), refetchInterval: 10000 });
  const profiles = useQuery({ queryKey: ["egress-profiles", server.id], queryFn: () => get<EgressProfile[]>(`/api/v1/servers/${server.id}/egress-profiles`) });
  const saved = (op: NetworkOperation) => {
    setReceipt(op.id); setDraft(null);
    for (const key of ["server-forwards", "network-operations", "egress-profile"]) void qc.invalidateQueries({ queryKey: [key] });
  };
  return <div className="mt-4 space-y-4">
    <Card><CardHeader className="flex-row items-center justify-between gap-3"><CardTitle>端口转发规则</CardTitle><Button onClick={() => setDraft({ initial: null })}>添加转发</Button></CardHeader><CardContent className="space-y-4">
      <p className="text-sm text-muted-foreground">有人连接这台 VPS 的指定端口时，将连接送到固定目标。它与节点中转无关。</p>
      <details className="text-xs text-muted-foreground"><summary className="cursor-pointer">协议与安全限制</summary><p className="mt-2">支持 TCP 固定转发，可选择直连、SOCKS5、SSH、WireGuard 或 SS-2022 出站规则。UDP 固定转发尚未通过官方内核的空闲会话回收验证，暂不可启用。SS-2022 出口需选择官方 sing-box 1.14.1 或兼容的 1.14.x 稳定版。TCP 半关闭行为由官方内核决定，依赖发送结束后继续接收响应的应用需先验证。域名目标仅适用于直连规则，私网目标需独立本机授权。还可限制来源地址和连接数。</p></details>
      {server.agent?.apply_error && <p role="alert" className="break-all text-sm text-destructive">服务器最近应用失败：{server.agent.apply_error}</p>}
      {forwards.isPending ? <Spinner /> : forwards.isError ? <p role="alert" className="text-destructive">{forwards.error.message}</p> : !forwards.data.length ? <Empty title="尚无固定转发" description="创建入口，并填写目标服务器的 IP 和端口。" /> : <div className="space-y-3">{forwards.data.map((f) => <div key={f.id} className="space-y-2 rounded-lg border p-4">
        <div className="flex flex-wrap items-center justify-between gap-2"><h3 className="font-medium">{f.name} <span className="text-xs text-muted-foreground">#{f.id} · 版本 {f.revision}</span></h3><Badge variant={f.retired ? "warning" : f.enabled ? "outline" : "secondary"}>{f.retired ? "等待清理回执" : f.enabled ? "配置：启用" : "配置：停用"}</Badge></div>
        <p className="break-all text-sm">{f.config.network.toUpperCase()} · {endpoint(f.config.listen_address || "全部本机地址", f.config.listen_port)} → {endpoint(f.config.target_host, f.config.target_port)}</p>
        <p className="break-all text-xs text-muted-foreground">来源：{f.config.source_mode === "all" ? "任意来源" : f.config.source_cidrs.length ? f.config.source_cidrs.join("、") : "不允许任何来源"}{f.config.network !== "udp" ? ` · 最多 ${f.config.max_tcp_connections} 个 TCP 连接` : ""}{f.config.network !== "tcp" ? ` · 最多 ${f.config.max_udp_sessions} 个 UDP 会话，空闲 ${f.config.udp_idle_seconds} 秒` : ""}{f.config.egress_profile_id ? ` · 出口 #${f.config.egress_profile_id} 固定版本 ${f.config.egress_revision}` : " · 按宿主机路由"}</p>
        <p className="text-xs text-muted-foreground">近 30 个 UTC 日期：入 {fmtBytes(f.rx_30_days)} · 出 {fmtBytes(f.tx_30_days)} · 汇总 {fmtBytes(f.rx_30_days + f.tx_30_days)}；独立统计，不重复计入分享。</p>
        {f.reserved_ports?.length > 1 && <p className="text-xs text-amber-700">仍保留的监听端口：{f.reserved_ports.join("、")}；旧端口等待清理确认。</p>}
        {server.diagnostics?.network_forward_errors?.[f.id] && <p role="alert" className="text-sm text-destructive">最近诊断：{server.diagnostics.network_forward_errors[f.id]}</p>}
        {f.retired ? <p className="text-xs text-muted-foreground">agent 关闭监听、结算流量后自动移除，并释放端口。</p> : <div className="flex gap-2"><Button size="sm" variant="outline" onClick={() => setDraft({ initial: f })}>编辑</Button><Button size="sm" variant="outline" onClick={() => setDraft({ initial: f, deleting: true })}>删除</Button></div>}
      </div>)}</div>}
    </CardContent></Card>
    {receipt && <NetworkOperationReceipt key={receipt} id={receipt} />}
    <details className="rounded-xl border bg-card p-4"><summary className="cursor-pointer font-medium">查看网络变更记录</summary><div className="mt-4"><NetworkOperations serverID={server.id} /></div></details>
    {draft && <ForwardEditor serverID={server.id} draft={draft} inventory={inventory.data} profiles={profiles.data ?? []} onClose={() => setDraft(null)} onSaved={saved} />}
  </div>;
}

function ForwardEditor({ serverID, draft: { initial, deleting }, inventory, profiles, onClose, onSaved }: { serverID: number; draft: Draft; inventory?: NetworkView; profiles: EgressProfile[]; onClose: () => void; onSaved: (op: NetworkOperation) => void }) {
  const toast = useToast();
  const [name, setName] = React.useState(initial?.name ?? "");
  const [enabled, setEnabled] = React.useState(initial?.enabled ?? true);
  const [config, setConfig] = React.useState<ForwardConfig>(initial?.config ?? { listen_mode: "all", listen_port: 25000, network: "tcp", target_host: "", target_port: 443, source_mode: "cidr", source_cidrs: [], max_tcp_connections: 128, max_udp_sessions: 0, udp_idle_seconds: 0 });
  const [cidrs, setCIDRs] = React.useState(config.source_cidrs.join("\n"));
  const [review, setReview] = React.useState<{ request: ForwardWrite; result: NetworkReadiness } | null>(null);
  const action = deleting ? "delete" : initial ? "update" : "create";
  const path = initial ? `/api/v1/forwards/${initial.id}` : `/api/v1/servers/${serverID}/forwards`;
  const patch = (value: Partial<ForwardConfig>) => setConfig({ ...config, ...value });
  const preview = useMutation({ mutationFn: async () => {
    const body = { expected_revision: initial?.revision ?? 0, ...(deleting ? {} : { name: name.trim(), enabled, config: { ...config, target_host: config.target_host.trim(), source_cidrs: config.source_mode === "all" ? [] : cidrs.split(/[\s,]+/).filter(Boolean) } }) };
    const result = await post<NetworkReadiness>(`${path}/preview`, { ...body, action });
    return { request: { ...body, operation_id: networkOperationID(), expected_impact: result.impact?.token }, result };
  }, onSuccess: setReview, onError: (e) => toast.fromError(e, "无法预览转发变更") });
  const save = useMutation({ mutationFn: () => api<NetworkOperation>(path, { method: deleting ? "DELETE" : initial ? "PUT" : "POST", json: review!.request }), onSuccess: onSaved, onError: (e) => toast.fromError(e) });
  const busy = preview.isPending || save.isPending;
  const validPort = (n: number) => Number.isInteger(n) && n >= 1 && n <= 65535;
  const valid = deleting || (name.trim() !== "" && config.target_host.trim() !== "" && validPort(config.listen_port) && validPort(config.target_port) && (config.listen_mode !== "address" || !!config.listen_address));
  return <Dialog open size="md" title={deleting ? `删除转发 · ${name}` : initial ? `编辑转发 · ${name}` : "添加端口转发"} onClose={() => { if (!busy) onClose(); }} footer={<>
    <Button variant="outline" disabled={busy} onClick={onClose}>关闭</Button>
    {review ? <><Button variant="outline" disabled={busy || networkRequestUncertain(save.error)} onClick={() => { setReview(null); save.reset(); }}>返回编辑</Button><Button variant={deleting ? "destructive" : "default"} disabled={!review.result.ready || !review.result.impact} loading={save.isPending} onClick={() => save.mutate()}>{save.isError ? "重试同一请求" : deleting ? "提交删除" : "提交转发变更"}</Button></> : <Button disabled={!valid} loading={preview.isPending} onClick={() => preview.mutate()}>预览变更</Button>}
  </>}>
    {review ? <div className="space-y-4 text-sm">
      {!deleting && <p className="break-all">{review.request.name} · {endpoint(review.request.config!.listen_address || "全部本机地址", review.request.config!.listen_port)} → {endpoint(review.request.config!.target_host, review.request.config!.target_port)} · {review.request.enabled ? "启用" : "停用"}</p>}
      <NetworkChecks view={review.result} />{review.result.impact && <NetworkImpactPreview impact={review.result.impact} />}
      <p>提交后等待 agent 应用回执。旧监听端口在清理确认前保持占用。</p>
      {save.isError && <p role="alert" className="text-destructive">{save.error.message}。{networkRequestUncertain(save.error) ? "请重试同一请求确认结果。" : "请返回编辑并重新预览。"}</p>}
    </div> : deleting ? <p className="text-sm">将停用并删除此转发。关闭监听和结算流量后才释放端口；同一代理进程中的连接可能中断。</p> : <fieldset disabled={busy} className="space-y-4">
      <Field label="转发名称"><Input aria-label="转发名称" value={name} maxLength={128} onChange={(e) => setName(e.target.value)} /></Field>
      <div className="grid gap-3 sm:grid-cols-2"><Field label="这台 VPS 的端口"><Input aria-label="监听端口" type="number" min={1} max={65535} value={config.listen_port} onChange={(e) => patch({ listen_port: Number(e.target.value) })} /></Field><Field label="连接类型"><Select aria-label="传输类型" value={config.network} onChange={(e) => { const network = e.target.value as ForwardConfig["network"]; patch({ network, max_tcp_connections: network === "udp" ? 0 : config.max_tcp_connections || 128, max_udp_sessions: network === "tcp" ? 0 : config.max_udp_sessions || 128, udp_idle_seconds: network === "tcp" ? 0 : config.udp_idle_seconds || 30 }); }}><option value="tcp">TCP</option><option value="udp" disabled>UDP（暂不可启用）</option><option value="both" disabled>TCP + UDP（暂不可启用）</option></Select></Field></div>
      <div className="grid gap-3 sm:grid-cols-[2fr_1fr]"><Field label="转发到哪台机器？"><Input aria-label="目标 IP 或域名" value={config.target_host} onChange={(e) => patch({ target_host: e.target.value })} /></Field><Field label="目标端口"><Input aria-label="目标端口" type="number" min={1} max={65535} value={config.target_port} onChange={(e) => patch({ target_port: Number(e.target.value) })} /></Field></div>
      <Field label="谁能连接这个端口？"><Select aria-label="允许的来源" value={config.source_mode} onChange={(e) => patch({ source_mode: e.target.value as "cidr" | "all" })}><option value="cidr">仅指定 IP 地址</option><option value="all">任何人</option></Select></Field>
      {config.source_mode === "cidr" ? <Field label="允许访问的地址段" hint="每行一个 CIDR；单个 IPv4 地址末尾加 /32。留空时任何人都无法连接。"><Textarea aria-label="来源 CIDR" rows={3} value={cidrs} onChange={(e) => setCIDRs(e.target.value)} /></Field> : <p className="text-sm text-amber-700">任何人都能连接这台 VPS 的这个端口。</p>}
      <details className="rounded-lg border p-3"><summary className="cursor-pointer text-sm font-medium">高级设置：监听地址、出口与连接数</summary><div className="mt-4 space-y-4">
        <Field label="监听范围"><Select aria-label="监听范围" value={config.listen_mode} onChange={(e) => patch({ listen_mode: e.target.value as "all" | "address", listen_address: undefined, listen_interface_id: undefined })}><option value="all">全部本机地址</option><option value="address">指定网卡地址</option></Select></Field>
        {config.listen_mode === "address" && <Field label="监听地址"><NetworkAddressSelect optional={false} label="监听地址" inventory={inventory} value={config.listen_address ? { interface_id: config.listen_interface_id!, address: config.listen_address } : undefined} onChange={(v) => patch({ listen_address: v?.address, listen_interface_id: v?.interface_id })} /></Field>}
        <Field label="业务出口" hint="选择已有出口后会固定使用当前版本，不会自动跟随模板更新。"><Select aria-label="业务出口" value={config.egress_profile_id ?? ""} onChange={(e) => { const p = profiles.find((p) => p.id === Number(e.target.value)); patch({ egress_profile_id: p?.id, egress_revision: p?.current_revision }); }}><option value="">按宿主机路由</option>{config.egress_profile_id && !profiles.some((p) => p.id === config.egress_profile_id) && <option value={config.egress_profile_id}>出口 #{config.egress_profile_id}</option>}{profiles.filter((p) => ["direct", "socks5", "ssh", "wireguard", "ss2022"].includes(p.kind)).map((p) => <option key={p.id} value={p.id} disabled={!p.enabled && p.id !== config.egress_profile_id}>{p.name}（{p.kind}）{!p.enabled ? "（已停用）" : ""}</option>)}</Select></Field>
        {!!config.egress_profile_id && <Field label="固定出口版本"><Input aria-label="固定出口版本" type="number" min={1} max={profiles.find((p) => p.id === config.egress_profile_id)?.current_revision} value={config.egress_revision ?? 1} onChange={(e) => patch({ egress_revision: Number(e.target.value) })} /></Field>}
        {config.network !== "udp" && <Field label="最大 TCP 连接数"><Input aria-label="最大 TCP 连接数" type="number" min={1} max={4096} value={config.max_tcp_connections} onChange={(e) => patch({ max_tcp_connections: Number(e.target.value) })} /></Field>}
        {config.network !== "tcp" && <div className="grid gap-3 sm:grid-cols-2"><Field label="最大 UDP 会话数"><Input aria-label="最大 UDP 会话数" type="number" min={1} max={4096} value={config.max_udp_sessions} onChange={(e) => patch({ max_udp_sessions: Number(e.target.value) })} /></Field><Field label="UDP 空闲超时（秒）"><Input aria-label="UDP 空闲超时（秒）" type="number" min={2} max={600} value={config.udp_idle_seconds} onChange={(e) => patch({ udp_idle_seconds: Number(e.target.value) })} /></Field></div>}
        <p className="text-xs text-muted-foreground">域名目标需要带 DNS 的直连出口；私网目标须先在 VPS 本机配置对应授权，再启用转发。</p>
      </div></details>
      <Switch checked={enabled} onChange={setEnabled} label="启用转发" />
    </fieldset>}
    {preview.isError && !review && <p role="alert" className="mt-3 text-sm text-destructive">{preview.error.message}</p>}
  </Dialog>;
}
