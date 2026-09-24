import * as React from "react";
import { useMutation } from "@tanstack/react-query";
import { post, put } from "@/lib/api";
import { isWireGuard, networkOperationID, networkRequestUncertain, type EgressPreview, type NetworkOperation, type WireGuardEgress } from "@/lib/network";
import { Button, Dialog, Field, Input, Select, Switch, Textarea } from "@/components/ui";
import { DNSFields, FamilyField, PathFields, type EgressEditorProps } from "@/components/egress-editor";
import { NetworkImpactPreview } from "@/components/network-impact";
import { useToast } from "@/components/toast";

const emptyConfig = (): WireGuardEgress => ({ server: "", server_port: 51820, public_key: "", addresses: [], allowed_ips: ["0.0.0.0/0"], mtu: 1408, persistent_keepalive: 25, family: "ipv4", dns: { transport: "udp", address: "", port: 53 }, outer: { family: "dual", dns: { transport: "udp", address: "", port: 53 } }, connect_timeout_seconds: 10 });
type Write = { operation_id: string; expected_revision: number; expected_impact?: string; name: string; kind: "wireguard"; enabled: boolean; config: WireGuardEgress; credentials?: { wireguard_private_key: string; wireguard_preshared_key: string } };

export function WireGuardEgressEditor({ serverID, initial, currentRevision, inventory, onClose, onSaved, onBack }: EgressEditorProps & { onBack: () => void }) {
  const [name, setName] = React.useState(initial?.profile.name ?? "");
  const [enabled, setEnabled] = React.useState(initial?.profile.enabled ?? true);
  const [config, setConfig] = React.useState<WireGuardEgress>(initial && isWireGuard(initial.revision.config) ? initial.revision.config : emptyConfig());
  const [mode, setMode] = React.useState(initial?.revision.has_credentials ? "retain" : "replace");
  const [key, setKey] = React.useState("");
  const [psk, setPSK] = React.useState("");
  const [review, setReview] = React.useState<{ request: Write; result: EgressPreview } | null>(null);
  const toast = useToast();
  const patch = (v: Partial<WireGuardEgress>) => setConfig({ ...config, ...v });
  const preview = useMutation({ gcTime: 0, mutationFn: async () => {
    const request: Write = { operation_id: networkOperationID(), expected_revision: initial?.profile.current_revision ?? 0, name: name.trim(), kind: "wireguard", enabled, config: { ...config, server: config.server.trim(), public_key: config.public_key.trim(), addresses: config.addresses.map(v => v.trim()).filter(Boolean), allowed_ips: config.allowed_ips.map(v => v.trim()).filter(Boolean) }, ...(mode === "replace" ? { credentials: { wireguard_private_key: key.trim(), wireguard_preshared_key: psk.trim() } } : {}) };
    const { operation_id: _operationID, ...body } = request;
    const result = await post<EgressPreview>(initial ? `/api/v1/egress-profiles/${initial.profile.id}/preview` : `/api/v1/servers/${serverID}/egress-profiles/preview`, { ...body, action: initial ? "update" : "create" });
    return { request: { ...request, expected_impact: result.impact.token }, result };
  }, onSuccess: setReview, onError: e => toast.fromError(e, "WireGuard 出口配置无效") });
  const save = useMutation({ gcTime: 0, mutationFn: () => initial ? put<NetworkOperation>(`/api/v1/egress-profiles/${initial.profile.id}`, review!.request) : post<NetworkOperation>(`/api/v1/servers/${serverID}/egress-profiles`, review!.request), onSuccess: onSaved, onError: e => toast.fromError(e) });
  const changed = !!initial && currentRevision !== undefined && currentRevision !== initial.profile.current_revision;
  const busy = preview.isPending || save.isPending;
  const ready = !!name.trim() && !!config.server.trim() && !!config.public_key.trim() && !!config.dns.address.trim() && !!config.outer.dns.address.trim() && config.addresses.some(v => v.trim()) && (mode === "retain" || !!key.trim());
  return <Dialog open title={initial ? `编辑 WireGuard 出口 · ${initial.profile.name}` : "创建 WireGuard 出口"} size="md" onClose={() => { if (!busy) onClose(); }} footer={<>
    <Button variant="outline" disabled={busy} onClick={onClose}>关闭</Button>
    {review ? <><Button variant="outline" disabled={busy || networkRequestUncertain(save.error)} onClick={() => { setReview(null); save.reset(); }}>返回编辑</Button><Button loading={save.isPending} disabled={!review.result.ready || changed && !networkRequestUncertain(save.error)} onClick={() => save.mutate()}>{save.isError ? "重试同一请求" : "保存出口版本"}</Button></> : <Button loading={preview.isPending} disabled={!ready || changed} onClick={() => preview.mutate()}>预览变更</Button>}
  </>}>
    {changed && <p role="alert" className="mb-3 text-sm text-destructive">出口版本已变化，请重新打开核对；请求结果不确定时可以重试同一请求。</p>}
    {review ? <div className="space-y-4 text-sm">
      {!review.result.ready && <p role="alert" className="text-destructive">{review.result.issues.join("；")}</p>}
      <p>{review.request.name} · {review.request.config.server}:{review.request.config.server_port} · {enabled ? "启用" : "停用"}</p>
      <p>已绑定节点保持原版本。每个节点必须使用自己的私钥和远端 Peer；相同私钥不能转给另一节点。</p>
      <NetworkImpactPreview impact={review.result.impact} />
      {save.isError && <p role="alert" className="text-destructive">{save.error.message}</p>}
    </div> : <fieldset disabled={busy} className="space-y-4">
      {!initial && <Button variant="ghost" size="sm" onClick={onBack}>选择其他出口类型</Button>}
      <p className="text-sm text-muted-foreground">连接已有 WireGuard Peer，承载节点 TCP 和 UDP 流量。仅管理该节点的用户态隧道，不修改宿主机默认路由。请先在远端配置此节点的独立公钥与隧道地址。</p>
      <Field label="出口名称"><Input aria-label="WireGuard 出口名称" value={name} onChange={e => setName(e.target.value)} /></Field>
      <div className="grid gap-3 sm:grid-cols-[2fr_1fr]"><Field label="Peer IP / 域名"><Input aria-label="WireGuard Peer 地址" value={config.server} onChange={e => patch({ server: e.target.value })} /></Field><Field label="Peer UDP 端口"><Input aria-label="WireGuard Peer 端口" type="number" min={1} max={65535} value={config.server_port} onChange={e => patch({ server_port: Number(e.target.value) })} /></Field></div>
      <Field label="远端 Peer 公钥"><Input aria-label="WireGuard Peer 公钥" value={config.public_key} onChange={e => patch({ public_key: e.target.value })} /></Field>
      {initial?.revision.has_credentials && <Field label="密钥处理"><Select aria-label="WireGuard 密钥处理" value={mode} onChange={e => { setMode(e.target.value); setKey(""); setPSK(""); }}><option value="retain">保留当前版本密钥</option><option value="replace">更换密钥</option></Select></Field>}
      {mode === "replace" && <><Field label="本节点私钥"><Input aria-label="WireGuard 本节点私钥" type="password" autoComplete="new-password" value={key} onChange={e => setKey(e.target.value)} /></Field><Field label="预共享密钥（可选）"><Input aria-label="WireGuard 预共享密钥" type="password" autoComplete="new-password" value={psk} onChange={e => setPSK(e.target.value)} /></Field></>}
      <p className="text-xs text-muted-foreground">密钥加密保存，不进入客户端订阅。换私钥后需要在远端同步更新 Peer 公钥。</p>
      <FamilyField label="业务地址族" value={config.family} onChange={family => patch({ family })} />
      <Field label="本地隧道地址" hint="每行一个 CIDR；双栈须各填一个 IPv4 / IPv6 地址。"><Textarea aria-label="WireGuard 隧道地址" value={config.addresses.join("\n")} onChange={e => patch({ addresses: e.target.value.split("\n") })} /></Field>
      <Field label="允许的目标网段" hint="每行一个 CIDR，须包含业务 DNS；未覆盖的目标不会回退直连。"><Textarea aria-label="WireGuard 目标网段" value={config.allowed_ips.join("\n")} onChange={e => patch({ allowed_ips: e.target.value.split("\n") })} /></Field>
      <div className="grid gap-3 sm:grid-cols-2"><Field label="MTU"><Input aria-label="WireGuard MTU" type="number" min={1280} max={9000} value={config.mtu} onChange={e => patch({ mtu: Number(e.target.value) })} /></Field><Field label="保活间隔（秒，0 关闭）"><Input aria-label="WireGuard 保活间隔" type="number" min={0} max={65535} value={config.persistent_keepalive} onChange={e => patch({ persistent_keepalive: Number(e.target.value) })} /></Field></div>
      <DNSFields prefix="业务 " value={config.dns} onChange={dns => patch({ dns })} />
      <div className="space-y-4 rounded-lg border p-3"><h3 className="text-sm font-semibold">连接 Peer 的外层路径</h3><PathFields outer inventory={inventory} value={config.outer} onChange={outer => patch({ outer })} /><DNSFields prefix="Peer 解析 " value={config.outer.dns} onChange={dns => patch({ outer: { ...config.outer, dns } })} /></div>
      <p className="text-xs text-muted-foreground">网卡、端点或 Peer 不可用时阻断业务。私网 Peer 需要本机独立 UDP 端口授权。</p>
      <Switch label="启用出口" checked={enabled} onChange={setEnabled} />
    </fieldset>}
  </Dialog>;
}
