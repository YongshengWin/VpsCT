import * as React from "react";
import { useMutation } from "@tanstack/react-query";
import { post, put } from "@/lib/api";
import { isSS2022, networkOperationID, networkRequestUncertain, type EgressPreview, type NetworkOperation, type SS2022Egress } from "@/lib/network";
import { Button, Dialog, Field, Input, Select, Switch } from "@/components/ui";
import { DNSFields, FamilyField, PathFields, type EgressEditorProps } from "@/components/egress-editor";
import { NetworkImpactPreview } from "@/components/network-impact";
import { useToast } from "@/components/toast";

const emptyConfig = (): SS2022Egress => ({ server: "", server_port: 8388, method: "2022-blake3-aes-128-gcm", family: "dual", dns: { transport: "udp", address: "", port: 53 }, outer: { family: "dual", dns: { transport: "udp", address: "", port: 53 } }, connect_timeout_seconds: 10 });
type Write = { operation_id: string; expected_revision: number; expected_impact?: string; name: string; kind: "ss2022"; enabled: boolean; config: SS2022Egress; credentials?: { username: string; password: string } };
const validKey = (value: string, method: SS2022Egress["method"]) => {
  try { const bytes = Uint8Array.from(atob(value), c => c.charCodeAt(0)); return btoa(String.fromCharCode(...bytes)) === value && bytes.length === (method.endsWith("128-gcm") ? 16 : 32); } catch { return false; }
};

export function SS2022EgressEditor({ serverID, initial, currentRevision, inventory, onClose, onSaved, onBack }: EgressEditorProps & { onBack: () => void }) {
  const [name, setName] = React.useState(initial?.profile.name ?? "");
  const [enabled, setEnabled] = React.useState(initial?.profile.enabled ?? true);
  const [config, setConfig] = React.useState<SS2022Egress>(initial && isSS2022(initial.revision.config) ? initial.revision.config : emptyConfig());
  const [mode, setMode] = React.useState(initial?.revision.has_credentials ? "retain" : "replace");
  const [key, setKey] = React.useState("");
  const [review, setReview] = React.useState<{ request: Write; result: EgressPreview } | null>(null);
  const toast = useToast();
  const patch = (v: Partial<SS2022Egress>) => setConfig({ ...config, ...v });
  const preview = useMutation({ gcTime: 0, mutationFn: async () => {
    const request: Write = { operation_id: networkOperationID(), expected_revision: initial?.profile.current_revision ?? 0, name: name.trim(), kind: "ss2022", enabled,
      config: { ...config, server: config.server.trim(), dns: { ...config.dns, address: config.dns.address.trim() }, outer: { ...config.outer, dns: { ...config.outer.dns, address: config.outer.dns.address.trim() } } },
      ...(mode === "replace" ? { credentials: { username: "", password: key.trim() } } : {}) };
    const { operation_id: _operationID, ...body } = request;
    const result = await post<EgressPreview>(initial ? `/api/v1/egress-profiles/${initial.profile.id}/preview` : `/api/v1/servers/${serverID}/egress-profiles/preview`, { ...body, action: initial ? "update" : "create" });
    return { request: { ...request, expected_impact: result.impact.token }, result };
  }, onSuccess: setReview, onError: e => toast.fromError(e, "SS-2022 出口配置无效") });
  const save = useMutation({ gcTime: 0, mutationFn: () => initial ? put<NetworkOperation>(`/api/v1/egress-profiles/${initial.profile.id}`, review!.request) : post<NetworkOperation>(`/api/v1/servers/${serverID}/egress-profiles`, review!.request), onSuccess: onSaved, onError: e => toast.fromError(e) });
  const changed = !!initial && currentRevision !== undefined && currentRevision !== initial.profile.current_revision;
  const busy = preview.isPending || save.isPending;
  const ready = !!name.trim() && !!config.server.trim() && !!config.dns.address.trim() && !!config.outer.dns.address.trim() && config.server_port > 0 && config.server_port <= 65535 && config.connect_timeout_seconds >= 1 && config.connect_timeout_seconds <= 60 && (mode === "retain" && !!initial?.revision.has_credentials || validKey(key, config.method));
  return <Dialog open title={initial ? `编辑 SS-2022 出口 · ${initial.profile.name}` : "创建 SS-2022 出口"} size="md" onClose={() => { if (!busy) onClose(); }} footer={<>
    <Button variant="outline" disabled={busy} onClick={onClose}>关闭</Button>
    {review ? <><Button variant="outline" disabled={busy || networkRequestUncertain(save.error)} onClick={() => { setReview(null); save.reset(); }}>返回编辑</Button><Button loading={save.isPending} disabled={!review.result.ready || changed && !networkRequestUncertain(save.error)} onClick={() => save.mutate()}>{save.isError ? "重试同一请求" : "保存出口版本"}</Button></> : <Button loading={preview.isPending} disabled={!ready || changed} onClick={() => preview.mutate()}>预览变更</Button>}
  </>}>
    {changed && <p role="alert" className="mb-3 text-sm text-destructive">出口版本已变化，请重新打开核对。</p>}
    {review ? <div className="space-y-4 text-sm">
      {!review.result.ready && <p role="alert" className="text-destructive">{review.result.issues.join("；")}</p>}
      <p>{review.request.name} · {review.request.config.server}:{review.request.config.server_port} · {enabled ? "启用" : "停用"}</p>
      <p>密钥不会显示在预览或订阅中；现有节点保持各自绑定的出口版本。</p>
      <NetworkImpactPreview impact={review.result.impact} />
      {save.isError && <p role="alert" className="text-destructive">{save.error.message}</p>}
    </div> : <fieldset disabled={busy} className="space-y-4">
      {!initial && <Button variant="ghost" size="sm" onClick={onBack}>选择其他出口类型</Button>}
      <p className="text-sm text-muted-foreground">连接已有 SS-2022 服务，承载 TCP 与 UDP。先在设置中选择官方 sing-box 1.14.1 或兼容的 1.14.x 稳定版；上游密钥须与服务端一致。</p>
      <Field label="出口名称"><Input aria-label="SS-2022 出口名称" value={name} onChange={e => setName(e.target.value)} /></Field>
      <div className="grid gap-3 sm:grid-cols-[2fr_1fr]"><Field label="上游 IP / 域名"><Input aria-label="SS-2022 上游地址" value={config.server} onChange={e => patch({ server: e.target.value })} /></Field><Field label="上游端口"><Input aria-label="SS-2022 上游端口" type="number" min={1} max={65535} value={config.server_port} onChange={e => patch({ server_port: Number(e.target.value) })} /></Field></div>
      <Field label="加密方法"><Select aria-label="SS-2022 加密方法" value={config.method} onChange={e => { patch({ method: e.target.value as SS2022Egress["method"] }); setMode("replace"); setKey(""); }}><option value="2022-blake3-aes-128-gcm">2022 BLAKE3 AES-128-GCM</option><option value="2022-blake3-aes-256-gcm">2022 BLAKE3 AES-256-GCM</option></Select></Field>
      {initial?.revision.has_credentials && <Field label="密钥处理"><Select aria-label="SS-2022 密钥处理" value={mode} onChange={e => { setMode(e.target.value); setKey(""); }}><option value="retain">保留当前版本密钥</option><option value="replace">更换密钥</option></Select></Field>}
      {mode === "replace" && <Field label="上游 Base64 密钥"><Input aria-label="SS-2022 上游密钥" type="password" autoComplete="new-password" value={key} onChange={e => setKey(e.target.value)} /></Field>}
      <FamilyField label="业务地址族" value={config.family} onChange={family => patch({ family })} />
      <DNSFields prefix="业务 " value={config.dns} onChange={dns => patch({ dns })} />
      <div className="space-y-4 rounded-lg border p-3"><h3 className="text-sm font-semibold">连接上游的外层路径</h3><PathFields outer inventory={inventory} value={config.outer} onChange={outer => patch({ outer })} /><DNSFields prefix="上游解析 " value={config.outer.dns} onChange={dns => patch({ outer: { ...config.outer, dns } })} /></div>
      <Field label="连接超时（秒）"><Input aria-label="SS-2022 连接超时" type="number" min={1} max={60} value={config.connect_timeout_seconds} onChange={e => patch({ connect_timeout_seconds: Number(e.target.value) })} /></Field>
      <p className="text-xs text-muted-foreground">私网上游须按节点、出口和 TCP/UDP 固定端口分别在 VPS 本机授权。密钥加密保存，不进入订阅。</p>
      <Switch label="启用出口" checked={enabled} onChange={setEnabled} />
    </fieldset>}
  </Dialog>;
}
