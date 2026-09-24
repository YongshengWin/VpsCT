import { WireGuardEgressEditor } from "@/components/wireguard-egress-editor";
import { SS2022EgressEditor } from "@/components/ss2022-egress-editor";
import * as React from "react";
import { useMutation } from "@tanstack/react-query";
import { post, put } from "@/lib/api";
import type { NetworkView } from "@/lib/types";
import { egressSummary, interfaceName, isUpstream, isSSH, networkOperationID, networkRequestUncertain, validSOCKS5Credentials, type DirectEgress, type EgressConfig, type EgressProfile, type EgressPreview, type EgressView, type NetworkOperation, type SOCKS5Credentials, type WireGuardEgress, type SS2022Egress } from "@/lib/network";
import { Button, Dialog, Field, Input, Select, Switch, Textarea } from "@/components/ui";
import { NetworkAddressSelect } from "@/components/network-address";
import { NetworkImpactPreview } from "@/components/network-impact";
import { useToast } from "@/components/toast";

type EgressWrite = { operation_id: string; expected_revision: number; expected_impact?: string; name: string; kind: EgressProfile["kind"]; enabled: boolean; config: EgressConfig; credentials?: SOCKS5Credentials };
const newDirect = (): DirectEgress => ({ family: "dual", dns: { transport: "udp", address: "", port: 53 } });
const validPort = (port: number) => Number.isInteger(port) && port >= 1 && port <= 65535;
const validDNS = (dns: DirectEgress["dns"]) => dns.address.trim() !== "" && validPort(dns.port);
const cleanDNS = (dns: DirectEgress["dns"]) => ({ ...dns, address: dns.address.trim() });

export function FamilyField({ value, label, onChange }: { value: DirectEgress["family"]; label: string; onChange: (v: DirectEgress["family"]) => void }) {
  return <Field label={label}><Select aria-label={label} value={value} onChange={(e) => onChange(e.target.value as DirectEgress["family"])}><option value="dual">IPv4 与 IPv6</option><option value="ipv4">仅 IPv4</option><option value="ipv6">仅 IPv6</option></Select></Field>;
}

export function DNSFields({ value, onChange, prefix = "", tcpOnly = false }: { value: DirectEgress["dns"]; onChange: (v: DirectEgress["dns"]) => void; prefix?: string; tcpOnly?: boolean }) {
  return <div className="grid gap-3 sm:grid-cols-[1fr_2fr_1fr]">
    <Field label={`${prefix}DNS 传输`}><Select aria-label={`${prefix}DNS 传输`} value={value.transport} onChange={(e) => onChange({ ...value, transport: e.target.value as "udp" | "tcp" })}><option value="udp" disabled={tcpOnly}>UDP</option><option value="tcp">TCP</option></Select></Field>
    <Field label={`${prefix}DNS 服务器 IP`}><Input aria-label={`${prefix}DNS 服务器 IP`} value={value.address} onChange={(e) => onChange({ ...value, address: e.target.value })} /></Field>
    <Field label={`${prefix}DNS 端口`}><Input aria-label={`${prefix}DNS 端口`} type="number" min={1} max={65535} value={value.port} onChange={(e) => onChange({ ...value, port: Number(e.target.value) })} /></Field>
  </div>;
}

export function PathFields({ value, onChange, inventory, outer = false }: { value: DirectEgress; onChange: (v: DirectEgress) => void; inventory?: NetworkView; outer?: boolean }) {
  const patch = (v: Partial<DirectEgress>) => onChange({ ...value, ...v });
  return <>
    <Field label={outer ? "连接上游的网卡" : "出口网卡"} hint="使用宿主机已有路由，不修改默认网关。仅绑定源 IP 时，实际出口仍由宿主机路由决定。"><Select aria-label={outer ? "连接上游的网卡" : "出口网卡"} value={value.interface_id ?? ""} onChange={(e) => patch({ interface_id: e.target.value || undefined, source_ipv4: undefined, source_ipv6: undefined })}>
      <option value="">按宿主机路由</option>
      {value.interface_id && !inventory?.interfaces.some((n) => n.interface.id === value.interface_id && n.present) && <option value={value.interface_id}>{interfaceName(inventory, value.interface_id)}</option>}
      {inventory?.interfaces.filter((n) => n.present && n.interface.kind !== "loopback").map((n) => <option key={n.interface.id} value={n.interface.id}>{n.interface.name}{n.interface.up && n.interface.carrier ? "" : "（链路不可用）"}</option>)}
    </Select></Field>
    <FamilyField label={outer ? "连接上游的地址族" : "业务地址族"} value={value.family} onChange={(family) => patch({ family, ...(family === "ipv4" ? { source_ipv6: undefined } : family === "ipv6" ? { source_ipv4: undefined } : {}) })} />
    {value.family !== "ipv6" && <Field label="IPv4 源地址"><NetworkAddressSelect label="IPv4 源地址" value={value.source_ipv4} onChange={(v) => patch({ source_ipv4: v })} inventory={inventory} interfaceID={value.interface_id} family="ipv4" /></Field>}
    {value.family !== "ipv4" && <Field label="IPv6 源地址"><NetworkAddressSelect label="IPv6 源地址" value={value.source_ipv6} onChange={(v) => patch({ source_ipv6: v })} inventory={inventory} interfaceID={value.interface_id} family="ipv6" /></Field>}
  </>;
}

export type EgressEditorProps = { serverID: number; initial: EgressView | null; currentRevision?: number; inventory?: NetworkView; onClose: () => void; onSaved: (op: NetworkOperation) => void };
export function EgressEditor(props: EgressEditorProps) {
  const [wireguard, setWireGuard] = React.useState(props.initial?.profile.kind === "wireguard");
  const [ss2022, setSS2022] = React.useState(props.initial?.profile.kind === "ss2022");
  return wireguard ? <WireGuardEgressEditor {...props} onBack={() => setWireGuard(false)} /> : ss2022 ? <SS2022EgressEditor {...props} onBack={() => setSS2022(false)} /> : <ProxyEgressEditor {...props} onWireGuard={() => setWireGuard(true)} onSS2022={() => setSS2022(true)} />;
}
function ProxyEgressEditor({ serverID, initial, currentRevision, inventory, onClose, onSaved, onWireGuard, onSS2022 }: EgressEditorProps & { onWireGuard: () => void; onSS2022: () => void }) {
  const [name, setName] = React.useState(initial?.profile.name ?? "");
  const [enabled, setEnabled] = React.useState(initial?.profile.enabled ?? true);
  const [config, setConfig] = React.useState<Exclude<EgressConfig, WireGuardEgress | SS2022Egress>>((initial?.revision.config as Exclude<EgressConfig, WireGuardEgress | SS2022Egress>) ?? newDirect());
  const canRetain = !!initial?.revision.has_credentials;
  const [credentialMode, setCredentialMode] = React.useState(canRetain ? "retain" : "replace");
  const [credentials, setCredentials] = React.useState<SOCKS5Credentials>({ username: "", password: "" });
  const [review, setReview] = React.useState<{ request: EgressWrite; result: EgressPreview } | null>(null);
  const socks = isUpstream(config);
  const ssh = isSSH(config);
  const transportName = ssh ? "SSH" : "SOCKS5";
  const toast = useToast();
  const preview = useMutation({ gcTime: 0, mutationFn: async () => {
    const cleaned: EgressConfig = isUpstream(config) ? { ...config, server: config.server.trim(), dns: cleanDNS(config.dns), outer: { ...config.outer, dns: cleanDNS(config.outer.dns) } } : { ...config, dns: cleanDNS(config.dns) };
    const body = { expected_revision: initial?.profile.current_revision ?? 0, name: name.trim(), kind: (ssh ? "ssh" : socks ? "socks5" : "direct") as EgressProfile["kind"], enabled, config: cleaned, ...(socks && config.authentication !== "none" && credentialMode === "replace" ? { credentials: { ...credentials } } : {}) };
    const result = await post<EgressPreview>(initial ? `/api/v1/egress-profiles/${initial.profile.id}/preview` : `/api/v1/servers/${serverID}/egress-profiles/preview`, { ...body, action: initial ? "update" : "create" });
    return { request: { ...body, operation_id: networkOperationID(), expected_impact: result.impact.token }, result };
  }, onSuccess: setReview, onError: (e) => toast.fromError(e, "无法预览出口变更") });
  const save = useMutation({ gcTime: 0, mutationFn: () => initial ? put<NetworkOperation>(`/api/v1/egress-profiles/${initial.profile.id}`, review!.request) : post<NetworkOperation>(`/api/v1/servers/${serverID}/egress-profiles`, review!.request), onSuccess: onSaved, onError: (e) => toast.fromError(e) });
  const changedWhileEditing = !!initial && currentRevision !== undefined && initial.profile.current_revision !== currentRevision;
  const changesRuntime = review?.result.impact.runtime_change ?? false;
  const ready = name.trim() !== "" && validDNS(config.dns) && (!socks || (config.server.trim() !== "" && validPort(config.server_port) && validDNS(config.outer.dns) && Number.isInteger(config.connect_timeout_seconds) && config.connect_timeout_seconds >= 1 && config.connect_timeout_seconds <= 60 && (config.authentication === "none" || (credentialMode === "retain" && canRetain) || (ssh ? credentials.username.trim() !== "" && (config.authentication === "private_key" ? !!credentials.private_key?.trim() : credentials.password !== "") : validSOCKS5Credentials(credentials)))));
  const validHostKeys = !ssh || config.host_keys.length > 0 && config.host_keys.every((v) => v.trim() !== "");
  const busy = preview.isPending || save.isPending;
  return <Dialog open title={initial ? `编辑出口 · ${initial.profile.name}` : "创建出口"} size="md" onClose={() => { if (!busy) onClose(); }} footer={<>
    <Button variant="outline" disabled={busy} onClick={onClose}>关闭</Button>
    {review ? <><Button variant="outline" disabled={busy || networkRequestUncertain(save.error)} onClick={() => { setReview(null); preview.reset(); save.reset(); }}>返回编辑</Button><Button loading={save.isPending} disabled={!review.result.ready || (changedWhileEditing && !networkRequestUncertain(save.error))} onClick={() => save.mutate()}>{save.isError ? "重试同一请求" : changesRuntime ? "提交出口变更" : "保存出口版本"}</Button></> : <Button loading={preview.isPending} disabled={!ready || !validHostKeys || changedWhileEditing} onClick={() => preview.mutate()}>预览变更</Button>}
  </>}>
    {changedWhileEditing && <p role="alert" className="mb-3 text-sm text-destructive">出口版本已变化。{networkRequestUncertain(save.error) ? "上次请求结果尚未确认，请重试同一请求核对结果。" : "请关闭并重新打开，核对最新版本后再编辑。"}</p>}
    {review ? <div className="space-y-4 text-sm">
      {!review.result.ready && <div role="alert" className="text-destructive"><p>当前变更尚不能提交</p><ul className="list-disc pl-5">{review.result.issues.map((issue, i) => <li key={i}>{issue}</li>)}</ul></div>}
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2"><dt>名称</dt><dd>{review.request.name}</dd><dt>出口</dt><dd className="break-all">{egressSummary(config, inventory)}</dd><dt>源地址</dt><dd className="break-all">{(socks ? config.outer : config).source_ipv4?.address || "IPv4 自动"} / {(socks ? config.outer : config).source_ipv6?.address || "IPv6 自动"}</dd>{socks && <><dt>上游解析 DNS</dt><dd className="break-all">{config.outer.dns.transport.toUpperCase()} · {config.outer.dns.address}:{config.outer.dns.port}</dd><dt>上游认证</dt><dd>{config.authentication === "none" ? "无认证" : credentialMode === "retain" ? "保留当前版本凭据" : "使用本次填写的新凭据"}</dd></>}<dt>目标状态</dt><dd>{enabled ? "启用" : "停用"}</dd></dl>
      <p>{initial ? `新增版本 ${initial.profile.current_revision + 1}。现有 ${review.result.impact.reference_count} 个引用保持各自原版本（包括认证信息）；只有在节点中明确改选版本才迁移。` : "保存为新出口模板；在节点中选择后才会影响业务。"}</p>
      <NetworkImpactPreview impact={review.result.impact} />
      {changesRuntime && <div className="rounded-lg border border-amber-500/40 bg-amber-500/5 p-3"><p>{enabled ? "恢复所有引用节点原来固定的出口版本；提交时重新检查旧版本的网卡及运行条件。" : "阻断所有引用节点的业务。服务器离线时仅保存停用意图，收到应用回执前业务可能继续运行。"}</p><p className="mt-2">应用可能重启共享代理进程，中断同一进程中的连接。</p></div>}
      {save.isError && <p role="alert" className="text-destructive">{save.error.message}。{networkRequestUncertain(save.error) ? "请求结果尚未确认；请重试同一请求核对结果。" : "请返回编辑，重新预览并核对后再提交。"}</p>}
    </div> : <fieldset disabled={busy} className="space-y-4">
      <Field label="出口名称"><Input aria-label="出口名称" value={name} maxLength={128} onChange={(e) => setName(e.target.value)} /></Field>
      <Field label="出口类型" hint={initial ? "类型不能在原出口上更换；请另建出口后重新绑定节点。" : undefined}><Select aria-label="出口类型" value={ssh ? "ssh" : socks ? "socks5" : "direct"} disabled={!!initial} onChange={(e) => { if (e.target.value === "wireguard") { onWireGuard(); return; } if (e.target.value === "ss2022") { onSS2022(); return; } setConfig(e.target.value === "direct" ? newDirect() : { server: "", server_port: e.target.value === "ssh" ? 22 : 1080, authentication: "password", ...(e.target.value === "ssh" ? { host_keys: [] } : {}), udp: false, family: "dual", dns: { transport: "tcp", address: "", port: 53 }, outer: newDirect(), connect_timeout_seconds: 10 }); setCredentials({ username: "", password: "" }); setCredentialMode("replace"); }}><option value="direct">直连</option><option value="socks5">SOCKS5 中转</option><option value="ssh">SSH 中转（TCP）</option><option value="wireguard">WireGuard 中转</option><option value="ss2022">SS-2022 中转</option></Select></Field>
      {socks ? <>
        <div className="grid gap-3 sm:grid-cols-[2fr_1fr]"><Field label={`${transportName} 上游 IP / 域名`}><Input aria-label={`${transportName} 上游 IP 或域名`} value={config.server} maxLength={253} onChange={(e) => setConfig({ ...config, server: e.target.value })} /></Field><Field label="上游端口"><Input aria-label="上游端口" type="number" min={1} max={65535} value={config.server_port} onChange={(e) => setConfig({ ...config, server_port: Number(e.target.value) })} /></Field></div>
        <p className="text-xs text-muted-foreground">连接已有 {transportName} 服务。私网上游需在服务器本机按节点、出口及端口授权，绑定时会检查；本机地址不可作为上游。</p>
        <Field label="上游认证"><Select aria-label="上游认证" value={config.authentication} onChange={(e) => { setConfig(ssh ? { ...config, authentication: e.target.value as "password" | "private_key" } : { ...config, authentication: e.target.value as "none" | "password" }); setCredentials({ username: "", password: "" }); setCredentialMode("replace"); }}><option value="password">用户名和密码</option>{ssh ? <option value="private_key">SSH 私钥</option> : <option value="none">无认证</option>}</Select></Field>
        {ssh && <Field label="可信 SSH 主机公钥" hint="通过可信渠道核对，每行一个完整公钥，不能留空或填写指纹。"><Textarea aria-label="可信 SSH 主机公钥" rows={3} value={config.host_keys.join("\n")} onChange={(e) => setConfig({ ...config, host_keys: e.target.value.split("\n") })} /></Field>}
        {config.authentication !== "none" && <>
          {canRetain && <Field label="认证信息"><Select aria-label="认证信息" value={credentialMode} onChange={(e) => { setCredentialMode(e.target.value); setCredentials({ username: "", password: "" }); }}><option value="retain">保留当前版本凭据</option><option value="replace">更换认证信息</option></Select></Field>}
          {credentialMode === "replace" && <div className="grid gap-3 sm:grid-cols-2"><Field label="上游用户名"><Input aria-label="上游用户名" autoComplete="off" value={credentials.username} onChange={(e) => setCredentials({ ...credentials, username: e.target.value })} /></Field>{ssh && config.authentication === "private_key" ? <><Field label="SSH 私钥"><Textarea aria-label="SSH 出口私钥" rows={5} value={credentials.private_key ?? ""} onChange={(e) => setCredentials({ ...credentials, private_key: e.target.value })} /></Field><Field label="私钥口令（可选）"><Input aria-label="SSH 出口私钥口令" type="password" autoComplete="new-password" value={credentials.private_key_passphrase ?? ""} onChange={(e) => setCredentials({ ...credentials, private_key_passphrase: e.target.value })} /></Field></> : <Field label="上游密码"><Input aria-label="上游密码" type="password" autoComplete="new-password" value={credentials.password} onChange={(e) => setCredentials({ ...credentials, password: e.target.value })} /></Field>}</div>}
          <p className="text-xs text-muted-foreground">{ssh ? "使用独立转发账号；粘贴内联私钥，不读取本机密钥文件。不会使用或修改 VPS 的管理 SSH 凭据。" : "用户名和密码各为 1–255 字节；前后空格会保留。"} 已保存的凭据不会显示，也不进入客户端订阅。</p>
        </>}
        <div className="space-y-4 rounded-lg border p-3"><h3 className="text-sm font-semibold">业务经过 {transportName}</h3>
          {!ssh && <Switch checked={config.udp} onChange={(udp) => setConfig({ ...config, udp, dns: { ...config.dns, transport: udp ? config.dns.transport : "tcp" } })} label="启用 UDP 转发" />}
          <p className="text-xs text-muted-foreground">{ssh ? "SSH 只支持 TCP；UDP 业务将被拒绝，业务 DNS 使用 TCP。" : "默认仅 TCP。UDP 需要上游支持且使用相同中继 IP；私网中继需独立授权端口范围。"}</p>
          <FamilyField label="业务地址族" value={config.family} onChange={(family) => setConfig({ ...config, family })} />
          <DNSFields prefix="业务 " value={config.dns} tcpOnly={!config.udp} onChange={(dns) => setConfig({ ...config, dns })} />
          <p className="text-xs text-muted-foreground">业务 DNS 经中转查询，填写目标 DNS 的 IP。</p>
        </div>
        <div className="space-y-4 rounded-lg border p-3"><h3 className="text-sm font-semibold">本机连接 {transportName} 上游</h3>
          <PathFields outer value={config.outer} inventory={inventory} onChange={(outer) => setConfig({ ...config, outer })} />
          <DNSFields prefix="上游解析 " value={config.outer.dns} onChange={(dns) => setConfig({ ...config, outer: { ...config.outer, dns } })} />
          <p className="text-xs text-muted-foreground">仅解析上游域名，经上方网卡和源地址查询，不用于业务域名。填写 DNS 的 IP；私网 DNS 需独立本机授权，UDP 查询还需允许同端口的 TCP 重试。上游是 IP 时无需查询。</p>
        </div>
        <Field label="连接超时（秒）"><Input aria-label="连接超时（秒）" type="number" min={1} max={60} value={config.connect_timeout_seconds} onChange={(e) => setConfig({ ...config, connect_timeout_seconds: Number(e.target.value) })} /></Field>
      </> : <><PathFields value={config} inventory={inventory} onChange={setConfig} /><DNSFields value={config.dns} onChange={(dns) => setConfig({ ...config, dns })} /><p className="text-xs text-muted-foreground">业务 DNS 与业务连接使用相同出口。填写字面量 IP；不能填域名，也不会回退到系统 DNS。</p></>}
      <p className="text-xs text-muted-foreground">网卡、源地址或上游失效时阻断相关节点，不自动改走其他出口。</p>
      <Switch checked={enabled} onChange={setEnabled} label="启用出口" />
    </fieldset>}
  </Dialog>;
}
