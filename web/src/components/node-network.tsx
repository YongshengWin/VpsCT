import { UnavailablePortWarning } from "@/components/unavailable-ports";
import type { UnavailablePort } from "@/lib/unavailable-ports";
import * as React from "react";
import { Link } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { get, post, put } from "@/lib/api";
import type { NetworkView, Node } from "@/lib/types";
import { canBindNodeNetwork, egressSummary, networkOperationID, networkRequestUncertain, type EgressProfile, type EgressRevision, type NetworkOperation, type NetworkReadiness, type NodeNetworkPolicy } from "@/lib/network";
import { Badge, Button, Field, Input, Select, Spinner, Textarea } from "@/components/ui";
import { NetworkAddressSelect } from "@/components/network-address";
import { NetworkChecks, NetworkOperationReceipt } from "@/components/network-operations";
import { useToast } from "@/components/toast";
import { NetworkImpactPreview } from "@/components/network-impact";

type BindingRequest = { operation_id: string; expected_revision: number; network: NodeNetworkPolicy | null; advertise_host?: string; expected_impact?: string; listen_port?: number };
type NetworkEditFocus = "port" | "access" | "reset" | "all";

type CountryOption = { code: string; name?: string };

function CountryMultiSelect({ options, value, onChange, loading, error }: { options: CountryOption[]; value: string[]; onChange: (value: string[]) => void; loading?: boolean; error?: string }) {
  const [open, setOpen] = React.useState(false);
  const [search, setSearch] = React.useState("");
  const root = React.useRef<HTMLDivElement>(null);
  React.useEffect(() => {
    if (!open) return;
    const close = (event: MouseEvent) => { if (!root.current?.contains(event.target as globalThis.Node)) setOpen(false); };
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [open]);
  const merged: CountryOption[] = [...options, ...value.filter(code => !options.some(option => option.code === code)).map(code => ({ code }))];
  const query = search.trim().toLocaleLowerCase();
  const filtered = merged.filter(option => !query || `${option.code} ${option.name ?? ""}`.toLocaleLowerCase().includes(query));
  const toggle = (code: string) => onChange(value.includes(code) ? value.filter(item => item !== code) : [...value, code]);
  return <div ref={root} className="relative">
    <button type="button" className="flex min-h-10 w-full items-center gap-2 rounded-xl border border-transparent bg-muted px-3.5 py-2 text-left text-sm hover:bg-accent focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-primary/25" onClick={() => setOpen(v => !v)} aria-haspopup="listbox" aria-expanded={open} disabled={loading}>
      <span className={value.length ? "min-w-0 flex-1 truncate" : "min-w-0 flex-1 truncate text-muted-foreground/70"}>{loading ? "正在读取当前地址库…" : value.length ? value.join(", ") : "选择国家 / 地区"}</span><span className="text-muted-foreground">⌄</span>
    </button>
    {open && <div className="absolute z-50 mt-2 w-full overflow-hidden rounded-2xl border border-border/60 bg-card p-2 shadow-lift">
      <Input autoFocus aria-label="搜索国家或地区代码" placeholder="搜索代码，例如 CN、US…" value={search} onChange={e => setSearch(e.target.value)} />
      <div className="mt-2 max-h-56 overflow-y-auto" role="listbox" aria-multiselectable="true">
        {error ? <p className="px-2 py-3 text-sm text-destructive">{error}</p> : filtered.length === 0 ? <p className="px-2 py-3 text-sm text-muted-foreground">没有可选国家，请先在“设置 → IP 数据”配置并刷新地址库。</p> : filtered.map(option => <label key={option.code} className="flex cursor-pointer items-center gap-2 rounded-lg px-2 py-2 text-sm hover:bg-accent"><input type="checkbox" checked={value.includes(option.code)} onChange={() => toggle(option.code)} className="h-4 w-4 accent-primary" /><span>{option.code}{option.name ? ` · ${option.name}` : ""}</span></label>)}
      </div>
      <div className="mt-2 flex items-center justify-between border-t border-border/60 px-1 pt-2 text-xs text-muted-foreground"><span>已选 {value.length} 个</span><button type="button" className="text-primary hover:underline disabled:opacity-40" disabled={!value.length} onClick={() => onChange([])}>清空</button></div>
    </div>}
  </div>;
}

function NodeNetworkEditor({ node, currentRevision, inventory, profiles, onClose, onSaved, egressOnly = false, focus = "all" }: { node: Node; currentRevision: number; inventory?: NetworkView; profiles: EgressProfile[]; onClose: () => void; onSaved: (op: NetworkOperation) => void; egressOnly?: boolean; focus?: NetworkEditFocus }) {
  const [mode, setMode] = React.useState<"binding" | "reset" | "keep">(egressOnly || focus === "access" ? "binding" : focus === "reset" ? "reset" : "keep");
  const [policy, setPolicy] = React.useState<NodeNetworkPolicy>(node.network ?? { listen_mode: "all", advertise_mode: "inherit", on_unavailable: "block" });
  const [port, setPort] = React.useState(String(node.listen_port || node.port));
  const [host, setHost] = React.useState(node.server);
  const [revision, setRevision] = React.useState(String(node.network?.egress_revision ?? ""));
  const [review, setReview] = React.useState<{ request: BindingRequest; result: NetworkReadiness } | null>(null);
  const toast = useToast();
  const ingressMode = policy.ingress?.mode ?? "none";
  const ipv6 = policy.ingress_family === "ipv6";
  const resetPolicy: NodeNetworkPolicy | null = node.network?.ingress_family === "ipv6"
    ? { ingress_family: "ipv6", listen_mode: "public_ipv6", listen_interface_id: node.network.listen_interface_id, advertise_mode: "inherit_ipv6", on_unavailable: "block" }
    : null;
  const unavailable = useQuery({ queryKey: ["unavailable-ports", node.server_id], queryFn: () => get<UnavailablePort[]>(`/api/v1/servers/${node.server_id}/unavailable-ports`), enabled: !!node.server_id && !egressOnly, refetchInterval: 10000 });
  const countryCatalog = useQuery({ queryKey: ["geoip-countries"], queryFn: () => get<{ countries: CountryOption[] }>("/api/v1/geoip/countries"), enabled: !egressOnly && mode === "binding" && ingressMode === "deny_countries", staleTime: 5 * 60 * 1000 });
  const selected = profiles.find((p) => p.id === policy.egress_profile_id);
  const historical = useQuery({ queryKey: ["egress-revision", policy.egress_profile_id, Number(revision)], enabled: mode === "binding" && !!policy.egress_profile_id && Number.isSafeInteger(Number(revision)) && Number(revision) > 0, queryFn: () => get<EgressRevision>(`/api/v1/egress-profiles/${policy.egress_profile_id}/revisions/${Number(revision)}`) });
  const stale = currentRevision !== (node.network_revision ?? 0);
  const request = (): BindingRequest => ({ ...(!egressOnly && mode !== "reset" && Number(port) !== node.listen_port ? { listen_port: Number(port) } : {}), operation_id: networkOperationID(), expected_revision: node.network_revision ?? 0, network: mode === "keep" ? node.network ?? null : mode === "reset" ? resetPolicy : { ...policy, ...(policy.egress_profile_id ? { egress_revision: Number(revision) } : {}) }, ...((mode === "binding" && policy.advertise_mode === "override") || (mode === "keep" && node.network?.advertise_mode === "override") ? { advertise_host: mode === "keep" ? node.server : host.trim() } : {}) });
  const preview = useMutation({ mutationFn: async () => {
    const candidate = request();
    const result = await post<NetworkReadiness>(`/api/v1/nodes/${node.id}/network/preview`, { network: candidate.network, ...(candidate.listen_port !== undefined ? { listen_port: candidate.listen_port } : {}), ...(candidate.advertise_host !== undefined ? { advertise_host: candidate.advertise_host } : {}) });
    if (!result.impact) throw new Error("控制端未返回影响范围，请更新控制端后重新预览");
    return { request: { ...candidate, network: result.network ?? candidate.network, expected_impact: result.impact.token }, result };
  }, onSuccess: setReview, onError: (e) => toast.fromError(e, "无法预览网络变更") });
  const save = useMutation({ mutationFn: () => put<NetworkOperation>(`/api/v1/nodes/${node.id}/network`, review!.request), onSuccess: onSaved, onError: (e) => toast.fromError(e) });
  const fieldsValid = (egressOnly || (Number.isInteger(Number(port)) && Number(port) >= 1 && Number(port) <= 65535)) && (mode === "keep" ? Number(port) !== node.listen_port : mode === "reset" || ((policy.listen_mode !== "address" || !!policy.listen_address) && (policy.advertise_mode !== "override" || !!host.trim()) && (ingressMode === "none" || (ingressMode === "relay_only" ? !!policy.ingress?.source_cidrs?.length : !!policy.ingress?.countries?.length)) && (!policy.egress_profile_id || (Number.isSafeInteger(Number(revision)) && Number(revision) >= 1 && !!selected && Number(revision) <= selected.current_revision && historical.isSuccess))));
  const editing = !review;
  const busy = preview.isPending || save.isPending;
  return <div className="space-y-4 rounded-xl border bg-muted/20 p-4">
    <div><p className="font-medium">{egressOnly ? (editing ? "选择节点出口" : "核对出口变更") : (editing ? focus === "port" ? "更换节点端口" : focus === "reset" ? "恢复默认网络" : "配置节点访问控制" : "核对节点网络变更")}</p><p className="mt-1 text-sm text-muted-foreground">{egressOnly ? "选择这台节点使用的业务出口。" : "先预览影响范围，确认后再提交；现有凭据和流量记录会保留。"}</p></div>
    {stale && <p role="alert" className="text-sm text-destructive">节点网络版本已变化。{networkRequestUncertain(save.error) ? "上次请求结果尚未确认，请重试同一请求核对结果。" : "请关闭编辑，读取最新版本后重新核对。"}</p>}
    {editing ? <fieldset disabled={busy} className="space-y-4">
      {!egressOnly && focus !== "access" && focus !== "reset" && <Field label="监听端口" hint="更换端口保留节点、凭据及流量记录。请先放行云安全组；NAT 主机只能使用商家已映射端口。"><Input aria-label="监听端口" type="number" min={1} max={65535} value={port} onChange={e => setPort(e.target.value)} /></Field>}
      {!egressOnly && <UnavailablePortWarning record={unavailable.data?.find(p => p.port === Number(port))} />}
      {!egressOnly && unavailable.isError && <p className="text-xs text-destructive">不可用端口记录暂时无法读取，预览时请再核对。</p>}
      {!egressOnly && focus === "all" && <div className="space-y-2"><p className="text-sm font-medium">变更内容</p><div className="grid gap-2 sm:grid-cols-3" role="radiogroup" aria-label="网络变更类型">
        {([[
          "keep", "更换端口", "保留当前访问控制"
        ], [
          "binding", "配置访问控制", "监听、来源与出口"
        ], [
          "reset", "恢复默认网络", "清除节点绑定"
        ]] as const).map(([value, label, description]) => <button key={value} type="button" role="radio" aria-checked={mode === value} onClick={() => setMode(value)} className={`rounded-lg border px-3 py-2 text-left transition-colors ${mode === value ? "border-primary bg-primary/5" : "border-border bg-background hover:bg-muted/50"}`}><span className="block text-sm font-medium">{label}</span><span className="mt-1 block text-xs text-muted-foreground">{description}</span></button>)}
      </div></div>}
      {mode === "keep" ? <p className="text-sm text-muted-foreground">保留当前监听地址、访问地址及出口，仅更换端口；客户端需要刷新订阅。</p> : mode === "reset" ? <p className="text-sm text-amber-700 dark:text-amber-300">申请撤销节点的监听和出口限制，恢复服务器默认网络与继承访问地址。必须等待服务器确认清理，清空表单不代表已经恢复。</p> : <>
        {!egressOnly && <>
          {node.core === "singbox" && <Field label="接入方式">
            <Select aria-label="接入方式" value={ipv6 ? "ipv6" : "default"} onChange={e => {
              const only = e.target.value === "ipv6";
              setPolicy({ ...policy, ingress_family: only ? "ipv6" : "default", listen_mode: only ? "public_ipv6" : "all",
                listen_address: undefined, listen_interface_id: undefined, advertise_mode: only ? "inherit_ipv6" : "inherit" });
            }}>
              <option value="default">普通接入</option><option value="ipv6">仅 IPv6 接入</option>
            </Select>
          </Field>}
          <Field label="节点监听">
            <Select aria-label="节点监听" value={policy.listen_mode} onChange={e => setPolicy({ ...policy, listen_mode: e.target.value as NodeNetworkPolicy["listen_mode"], listen_address: undefined, listen_interface_id: undefined })}>
              {ipv6 ? <option value="public_ipv6">跟随本机公网 IPv6</option> : <option value="all">全部本机地址</option>}
              <option value="address">{ipv6 ? "指定本机 IPv6" : "指定本机 IP"}</option>
            </Select>
          </Field>
          {policy.listen_mode === "address" && <Field label="监听 IP" hint="请选择 agent 上报的可用地址。">
            <NetworkAddressSelect label="监听 IP" optional={false} family={ipv6 ? "ipv6" : undefined} inventory={inventory}
              value={policy.listen_address ? { interface_id: policy.listen_interface_id!, address: policy.listen_address } : undefined}
              onChange={v => setPolicy({ ...policy, listen_address: v?.address, listen_interface_id: v?.interface_id })} />
          </Field>}
        </>}
        {!egressOnly && <>
          <Field label="入口来源限制" hint="只判断落地 VPS 实际看到的源 IP。仅允许中转模式是强保证；地区拒绝模式依赖 GeoIP 数据，不能识别被中转隐藏的原始客户端地区。">
            <Select aria-label="入口来源限制" value={ingressMode} onChange={(e) => {
              const value = e.target.value;
              setPolicy({ ...policy, ingress: value === "none" ? undefined : value === "relay_only" ? { mode: "relay_only", source_cidrs: [] } : { mode: "deny_countries", countries: [] } });
            }}>
              <option value="none">允许所有来源</option>
              <option value="relay_only">仅允许指定中转来源</option>
              <option value="deny_countries">拒绝指定国家 / 地区</option>
            </Select>
          </Field>
          {ingressMode === "relay_only" && <Field label="允许的中转出口 CIDR" hint="每行一个 IPv4/IPv6 网段；填写落地 VPS 实际看到的中转出口地址。"><Textarea rows={4} className="mono" value={(policy.ingress?.source_cidrs ?? []).join("\n")} onChange={(e) => setPolicy({ ...policy, ingress: { mode: "relay_only", source_cidrs: e.target.value.split(/[\s,]+/).filter(Boolean) } })} placeholder="203.0.113.10/32\n2001:db8::/64" /></Field>}
          {ingressMode === "deny_countries" && <><Field label="拒绝的国家 / 地区" hint={countryCatalog.isError ? "当前地址库不可用，请先到设置 → IP 数据配置并刷新。" : "从当前地址库选择；保存时控制端会固定数据源版本并物化 IPv4/IPv6 地址集合。"}><CountryMultiSelect options={countryCatalog.data?.countries ?? []} value={policy.ingress?.countries ?? []} onChange={countries => setPolicy({ ...policy, ingress: { mode: "deny_countries", countries, source_cidrs: policy.ingress?.source_cidrs } })} loading={countryCatalog.isPending} error={countryCatalog.isError ? "无法读取当前地址库" : undefined} /></Field><Field label="中转出口例外 CIDR" hint="可选；这些实际来源只跳过 GeoIP 国家拒绝，不绕过节点认证和其他网络保护。"><Textarea rows={3} className="mono" value={(policy.ingress?.source_cidrs ?? []).join("\n")} onChange={(e) => setPolicy({ ...policy, ingress: { mode: "deny_countries", countries: policy.ingress?.countries ?? [], source_cidrs: e.target.value.split(/[\s,]+/).filter(Boolean) } })} placeholder="203.0.113.10/32" /></Field></>}
        </>}
        <Field label="业务出口"><Select aria-label="节点业务出口" value={policy.egress_profile_id ?? ""} onChange={(e) => {
          const p = profiles.find((p) => p.id === Number(e.target.value));
          setPolicy({ ...policy, egress_profile_id: p?.id, egress_revision: p?.current_revision }); setRevision(String(p?.current_revision ?? ""));
        }}><option value="">使用宿主机默认出口</option>{policy.egress_profile_id && !selected && <option value={policy.egress_profile_id}>原出口 #{policy.egress_profile_id}（无法读取）</option>}{profiles.map((p) => <option key={p.id} value={p.id} disabled={node.core !== "singbox" || !p.enabled || (p.kind !== "direct" && !["ss", "vless", "trojan", "anytls", "hysteria2", "tuic"].includes(node.protocol)) || Boolean(p.managed_stage)}>{p.name}{p.enabled ? "" : "（已停用）"}</option>)}</Select></Field>
        {policy.egress_profile_id ? <div className="space-y-2"><Field label="固定出口版本" hint="新模板版本不会自动替换旧绑定。这里明确选择需要应用的版本。"><Input aria-label="固定出口版本" type="number" min={1} max={selected?.current_revision ?? 4096} value={revision} onChange={(e) => setRevision(e.target.value)} /></Field>{selected && <p className="text-xs text-muted-foreground">最新版本 {selected.current_revision}{!selected.enabled && " · 此出口已停用，需到服务器出口页恢复"}</p>}{historical.isFetching ? <Spinner /> : historical.isError ? <p className="text-sm text-destructive">所选版本读取失败：{historical.error.message}</p> : historical.data && <p className="text-xs text-muted-foreground">{egressSummary(historical.data.config, inventory)}</p>}</div> : null}
        {!egressOnly && <><Field label="客户端访问地址"><Select aria-label="客户端访问地址模式" value={policy.advertise_mode} onChange={(e) => setPolicy({ ...policy, advertise_mode: e.target.value as NodeNetworkPolicy["advertise_mode"] })}><option value={ipv6 ? "inherit_ipv6" : "inherit"}>{ipv6 ? "继承服务器 IPv6 地址 / DDNS" : "继承服务器访问地址"}</option><option value="override">此节点独立设置</option></Select></Field>
        {policy.advertise_mode === "override" && <Field label="独立访问 IP / 域名" hint="填写客户端可达的入口，不能包含协议、端口或路径；不会把出口地址写入订阅。"><Input aria-label="独立访问 IP 或域名" value={host} maxLength={253} onChange={(e) => setHost(e.target.value)} /></Field>}
        </>}
        {(selected?.kind === "socks5" || selected?.kind === "ssh") && <p className="text-xs text-muted-foreground">私网中转的本机授权须对应节点 #{node.id}、出口 #{selected.id}。上游连接与上游 DNS 分别授权；保存出口模板不会授予本机权限，也不会开放业务访问内网。</p>}
        <p className="text-xs text-muted-foreground">指定的网卡或源地址失效时阻断本节点，不自动改走其他出口。计费来源不随此设置改变。</p>
      </>}
    </fieldset> : <div className="space-y-3 text-sm">
      <NetworkChecks view={review.result} />
      {review.result.impact && <NetworkImpactPreview impact={review.result.impact} />}
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2"><dt>节点</dt><dd>{node.name} · #{node.id}</dd><dt>监听端口</dt><dd>{node.listen_port || node.port} → {review.request.listen_port ?? node.listen_port ?? node.port}</dd><dt>监听</dt><dd>{review.request.network ? review.request.network.listen_address || (review.request.network.listen_mode === "public_ipv6" ? "跟随本机公网 IPv6" : "全部本机地址") : "恢复默认监听"}</dd><dt>入口来源</dt><dd>{review.request.network?.ingress?.mode === "relay_only" ? `仅允许 ${(review.request.network.ingress.source_cidrs ?? []).length} 个中转网段` : review.request.network?.ingress?.mode === "deny_countries" ? `拒绝 ${(review.request.network.ingress.countries ?? []).join(", ")} · ${review.request.network.ingress.geo_version ?? "准备数据中"}` : "允许所有来源"}</dd><dt>出口</dt><dd>{review.request.network?.egress_profile_id ? `${selected?.name ?? "原出口"} · 固定版本 ${review.request.network.egress_revision}` : "宿主机默认出口"}</dd><dt>访问地址</dt><dd className="break-all">{review.request.advertise_host ?? "继承服务器访问地址"}</dd></dl>
      <p>应用可能重启同一共享代理进程，影响其中的现有连接。配置服务器网络不会改变节点凭据；访问地址改变后，客户端需要更新订阅。</p>
      {mode === "reset" && <p className="text-amber-700 dark:text-amber-300">确认撤销当前网络绑定并恢复默认路径，等待服务器完成清理。</p>}
      {save.isError && <p role="alert" className="text-destructive">{save.error.message}。{networkRequestUncertain(save.error) ? "请求结果尚未确认；请重试同一请求核对结果。" : "请返回编辑，重新预览并核对后再提交。"}</p>}
    </div>}
    <div className="flex flex-wrap justify-end gap-2"><Button size="sm" variant="outline" disabled={busy} onClick={onClose}>关闭编辑</Button>{review ? <><Button size="sm" variant="outline" disabled={busy || networkRequestUncertain(save.error)} onClick={() => { setReview(null); save.reset(); }}>返回编辑</Button><Button size="sm" disabled={!review.result.ready || (stale && !networkRequestUncertain(save.error))} loading={save.isPending} onClick={() => save.mutate()}>{save.isError ? "重试同一请求" : "提交网络变更"}</Button></> : <Button size="sm" disabled={!fieldsValid || stale} loading={preview.isPending} onClick={() => preview.mutate()}>预览变更</Button>}</div>
  </div>;
}

export function NodeNetwork({ nodeID, serverID, onNavigate, embedded = false }: { nodeID: number; serverID: number; onNavigate: () => void; embedded?: boolean }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [editing, setEditing] = React.useState<Node | null>(null);
  const [focus, setFocus] = React.useState<NetworkEditFocus>("all");
  const [receipt, setReceipt] = React.useState("");
  const refreshGeo = useMutation({ mutationFn: () => post<{ updated_nodes: number; version?: string }>("/api/v1/geoip/refresh", {}), onSuccess: (v) => { toast.success(`GeoIP 策略已刷新${v.version ? `（${v.version}）` : ""}`); void qc.invalidateQueries({ queryKey: ["nodes"] }); void qc.invalidateQueries({ queryKey: ["servers"] }); }, onError: (e) => toast.fromError(e) });
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
  const port = n.listen_port || n.port;
  const ingress = n.network?.ingress;
  const ingressLabel = ingress?.mode === "relay_only" ? "仅允许中转来源" : ingress?.mode === "deny_countries" ? `拒绝 ${(ingress.countries ?? []).join(", ") || "指定地区"}` : "允许所有来源";
  const ingressDetail = ingress?.mode === "relay_only" ? `${(ingress.source_cidrs ?? []).length} 个中转网段` : ingress?.mode === "deny_countries" ? `GeoIP 数据 ${ingress.geo_version ?? "待准备"}` : "当前未设置入口限制";
  const disabled = Boolean(p?.managed_stage && p.managed_stage !== "retired") || n.revoked || (!qualified && !n.network) || !profiles.isSuccess || !capabilities.isSuccess;
  const startEditing = (nextFocus: NetworkEditFocus) => { setFocus(nextFocus); setEditing(n); };
  const summaryCard = (nextFocus: NetworkEditFocus, title: string, value: string, detail: string) => {
    const content = <><p className="text-xs text-muted-foreground">{title}</p><p className="mt-1 text-lg font-semibold">{value}</p><p className="mt-1 text-xs text-muted-foreground">{detail}</p><p className="mt-2 text-xs font-medium text-primary">点击设置</p></>;
    return embedded ? <div className="rounded-lg border bg-muted/20 p-3">{content}</div> : <button type="button" className="rounded-lg border bg-muted/20 p-3 text-left transition-colors hover:border-primary/50 hover:bg-primary/5 disabled:cursor-not-allowed disabled:opacity-60" onClick={() => startEditing(nextFocus)} disabled={disabled || !!editing}>{content}</button>;
  };
  return <>
  <section id={!embedded ? `node-network-${nodeID}` : undefined} className="mt-4 space-y-4 rounded-xl border bg-card p-4">
    <div className="flex flex-wrap items-start justify-between gap-3">
      <div><h3 className="font-semibold">{embedded ? "将出口应用到节点" : "访问控制"}</h3><p className="mt-1 text-sm text-muted-foreground">{embedded ? "选择这台节点使用的业务出口。" : "控制哪些客户端可以连接这个节点；端口变更和访问控制分别提交。"}</p></div>
      {!embedded && <Link className="text-sm text-primary underline" to={`/servers/${serverID}?tab=egress`} onClick={onNavigate}>管理服务器出口</Link>}
    </div>
    <div className="grid gap-3 sm:grid-cols-2">
      {summaryCard("port", "接入端口", String(port), `监听 ${n.network?.listen_address || (n.network?.listen_mode === "public_ipv6" ? "跟随本机公网 IPv6" : "默认 / 全部本机地址")}`)}
      {summaryCard("access", "入口来源", embedded ? "按节点现有设置" : ingressLabel, embedded ? "出口应用不会改变入口策略" : ingressDetail)}
    </div>
    {!embedded && <div className="flex flex-wrap items-center gap-2"><Badge variant="outline">网络版本 {n.network_revision ?? 0}</Badge><Badge variant="secondary">{n.network ? "已指定网络" : "默认网络"}</Badge><span className="text-xs text-muted-foreground">出口：{n.network?.egress_profile_id ? `${p?.name ?? `出口 #${n.network.egress_profile_id}`} · 版本 ${n.network.egress_revision}` : "宿主机默认出口"}</span></div>}
    {n.network?.ingress?.mode === "deny_countries" && <p className="text-xs text-muted-foreground"><a className="underline" href="https://db-ip.com" target="_blank" rel="noreferrer">IP Geolocation by DB-IP</a> · 国家地址数据仅用于入口策略。</p>}
    {!editing && capabilities.data && !capabilities.data.ready && <NetworkChecks view={capabilities.data} />}
    {(inventory.isError || profiles.isError || capabilities.isError) && <p role="alert" className="text-sm text-destructive">网卡、出口或运行条件暂时无法读取，请刷新后再编辑。</p>}
    {!qualified && <p className="text-xs text-muted-foreground">该协议组合尚未开放新绑定；已有配置可申请清理。sing-box 的 SS、VLESS Reality、Trojan、AnyTLS、Hysteria2 和 TUIC 支持监听及出口绑定；非原有 SS-2022 AES-128 组合需新版 agent。独立内核只支持指定监听。</p>}
    {!editing && (embedded ? <Button size="sm" variant="outline" disabled={disabled} onClick={() => startEditing("all")}>选择并应用出口</Button> : <div className="flex flex-wrap gap-2">{n.network && <Button size="sm" variant="ghost" onClick={() => startEditing("reset")} disabled={disabled}>恢复默认网络</Button>}{n.network?.ingress?.mode === "deny_countries" && <Button size="sm" variant="ghost" loading={refreshGeo.isPending} onClick={() => refreshGeo.mutate()}>刷新 GeoIP 数据</Button>}</div>)}
    {editing && <NodeNetworkEditor egressOnly={embedded} focus={focus} node={editing} currentRevision={n.network_revision ?? 0} inventory={inventory.data} profiles={profiles.data ?? []} onClose={() => setEditing(null)} onSaved={saved} />}
    {receipt && <NetworkOperationReceipt id={receipt} />}
  </section>
  </>;
}
