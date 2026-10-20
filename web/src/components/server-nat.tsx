import * as React from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, Trash2 } from "lucide-react";
import { ApiError, get, put } from "@/lib/api";
import type { Server } from "@/lib/types";
import { emptyNATMapping, isNATServer, natDraft, natDraftConfig, natPortRange, natState, type NATConfig, type NATDraft, type NATView } from "@/lib/nat";
import { Badge, Button, Card, CardContent, CardHeader, CardTitle, Confirm, Dialog, Field, Input, Select, Spinner, Table, Td, Th, Tr } from "@/components/ui";
import { useToast } from "@/components/toast";

export function NATBadge({ server }: { server: Server }) {
  return isNATServer(server) ? <Badge variant="outline" title="NAT 端口映射；应用状态见端口映射页">NAT</Badge> : null;
}

export function NATStatus({ server, view }: { server: Server; view?: NATView }) {
  if (server.is_nat && server.nat?.status === "disabled" && !server.nat.pending && !view?.desired_config && !view?.report?.configured) return <Badge variant="warning">待配置映射</Badge>;
  const status = natState(server.agent_status, server.nat, view);
  return <Badge variant={status.variant}>{status.label}</Badge>;
}

function MappingTable({ config }: { config?: NATConfig | null }) {
  if (!config?.mappings.length) return <p className="text-sm text-muted-foreground">没有端口映射。</p>;
  return <div className="space-y-3">
    <Table><thead><tr><Th>公网端口</Th><Th>本机端口</Th><Th>数量</Th><Th>传输</Th></tr></thead><tbody>{config.mappings.map((m, i) => <Tr key={i}><Td className="tabular-nums">{natPortRange(m.public_port, m.count)}</Td><Td className="tabular-nums">{natPortRange(m.local_port, m.count)}</Td><Td>{m.count || 1}</Td><Td>{m.network === "tcp" ? "TCP" : m.network === "udp" ? "UDP" : "TCP + UDP"}</Td></Tr>)}</tbody></Table>
    {(config.interface || config.local_address) && <p className="text-xs text-muted-foreground">{config.interface && `网卡 ${config.interface}`}{config.interface && config.local_address && " · "}{config.local_address && `本机 IPv4 ${config.local_address}`}</p>}
  </div>;
}

export function ServerNAT({ server }: { server: Server }) {
  const qc = useQueryClient(), toast = useToast();
  const q = useQuery({ queryKey: ["server-nat", server.id], queryFn: () => get<NATView>(`/api/v1/servers/${server.id}/nat`), refetchInterval: 10000 });
  const [edit, setEdit] = React.useState<NATView | null>(null);
  const [clear, setClear] = React.useState<NATView | null>(null);
  const saved = () => {
    toast.success("NAT 配置已保存，等待 agent 应用");
    setEdit(null); setClear(null);
    void qc.invalidateQueries({ queryKey: ["server-nat", server.id] });
    void qc.invalidateQueries({ queryKey: ["servers"] });
  };
  const remove = useMutation({ mutationFn: () => put<NATView>(`/api/v1/servers/${server.id}/nat`, { expected_revision: clear!.revision, config: null }), onSuccess: saved, onError: (e) => { toast.fromError(e); void qc.invalidateQueries({ queryKey: ["server-nat", server.id] }); } });
  const view = q.data;
  const local = view?.report?.source === "local" || server.nat?.source === "local";
  const editable = !q.isError && !!view?.editable && !!server.nat?.editable && server.agent_status === "online" && !local;
  const pending = !!view?.pending || !!server.nat?.pending;
  const error = !pending || local ? server.nat?.error || view?.report?.error : undefined;
  const reason = local ? "本机 root NAT 文件优先，面板只读" : view?.reason || server.nat?.reason;
  const config = local ? view?.report?.config : view?.desired_config;
  return <Card className="mt-4">
    <CardHeader className="flex-row flex-wrap items-center justify-between gap-3"><CardTitle>NAT 端口映射</CardTitle><div className="flex flex-wrap items-center gap-2"><NATStatus server={server} view={q.isError ? undefined : view} /><Button size="sm" variant="outline" disabled={!editable} onClick={() => setEdit(view!)}>配置 NAT</Button></div></CardHeader>
    <CardContent className="space-y-3">
      <p className="text-sm text-muted-foreground">请先在商家面板做好端口映射，再填写下方配置。节点与订阅使用公网端口，本机端口是商家转入 VPS 的实例端口。agent 管理连接无需映射端口。</p>
      {q.isPending ? <Spinner /> : q.isError ? <p role="alert" className="text-sm text-destructive">NAT 信息加载失败：{q.error.message}</p> : <>
        <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground"><span>{local ? "来源：本机 root 配置" : view?.desired_config ? "来源：面板配置" : view?.pending ? "等待清除面板配置" : "尚未配置 NAT"}{view?.pending && !local ? " · 此表为待应用配置" : ""}</span>{editable && view?.desired_config && <Button size="sm" variant="ghost" onClick={() => { remove.reset(); setClear(view); }}>清除面板配置</Button>}</div>
        <MappingTable config={config} />
      </>}
      {reason && <p className="text-sm text-muted-foreground">{reason}</p>}
      {error && <p role="alert" className="wrap-break-word text-sm text-destructive">{error}</p>}
      <p className="text-xs text-muted-foreground">“本机已应用”表示 agent 已确认端口适配；公网连通性仍需商家映射和实际连接验证。</p>
    </CardContent>
    {edit && <NATDialog server={server} initial={edit} editable={editable} onClose={() => setEdit(null)} onSaved={saved} />}
    <Confirm open={!!clear} onClose={() => { if (!remove.isPending) setClear(null); }} title="清除面板 NAT 配置？" description="请先停用依赖这些映射的节点和转发；清除后等待 agent 确认。" destructive loading={remove.isPending} onConfirm={() => { if (editable) remove.mutate(); }} />
  </Card>;
}

function NATDialog({ server, initial, editable, onClose, onSaved }: { server: Server; initial: NATView; editable: boolean; onClose: () => void; onSaved: () => void }) {
  const qc = useQueryClient();
  const [draft, setDraft] = React.useState(() => natDraft(initial.desired_config));
  let config: NATConfig | undefined, issue = "";
  try { config = natDraftConfig(draft); } catch (e) { issue = (e as Error).message; }
  const save = useMutation({ mutationFn: () => put<NATView>(`/api/v1/servers/${server.id}/nat`, { expected_revision: initial.revision, config }), onSuccess: onSaved, onError: (e) => { if (e instanceof ApiError && e.status === 409) { void qc.invalidateQueries({ queryKey: ["server-nat", server.id] }); void qc.invalidateQueries({ queryKey: ["servers"] }); } } });
  return <Dialog open size="md" title="配置 NAT 端口映射" onClose={() => { if (!save.isPending) onClose(); }} footer={<><Button variant="outline" disabled={save.isPending} onClick={onClose}>取消</Button><Button loading={save.isPending} disabled={!editable || !config} onClick={() => save.mutate()}>保存并下发</Button></>}>
    <fieldset disabled={save.isPending || !editable} className="space-y-4">
      <p className="text-sm text-muted-foreground">例如商家将公网 30001 转入本机 10001，节点端口填写 30001。连续映射只需填写起始端口和数量。</p>
      <NATMappingFields draft={draft} onChange={setDraft} />
    </fieldset>
    {issue && <p className="mt-3 text-sm text-muted-foreground">{issue}</p>}
    {!editable && <p role="alert" className="mt-3 text-sm text-destructive">当前不能修改 NAT 配置，请关闭后核对服务器状态。</p>}
    {save.isError && <p role="alert" className="mt-3 text-sm text-destructive">{save.error.message}。配置版本冲突时，请关闭后重新打开编辑。</p>}
  </Dialog>;
}

export function NATMappingFields({ draft, onChange }: { draft: NATDraft; onChange: React.Dispatch<React.SetStateAction<NATDraft>> }) {
  const setDraft = onChange;
  const patch = (i: number, value: Partial<NATDraft["mappings"][number]>) => setDraft((p) => ({ ...p, mappings: p.mappings.map((m, n) => n === i ? { ...m, ...value } : m) }));
  return <div className="space-y-3">
      {draft.mappings.map((m, i) => <div key={i} className="rounded-lg border p-3"><div className="mb-2 flex items-center justify-between"><span className="text-xs text-muted-foreground">映射 {i + 1}</span><Button size="sm" variant="ghost" aria-label={`删除第 ${i + 1} 条映射`} onClick={() => setDraft((p) => ({ ...p, mappings: p.mappings.filter((_, n) => n !== i) }))}><Trash2 className="h-4 w-4" /></Button></div><div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <Field label="公网端口"><Input aria-label={`第 ${i + 1} 条公网端口`} type="number" min={1} max={65535} step={1} placeholder="30001" value={m.public_port} onChange={(e) => patch(i, { public_port: e.target.value })} /></Field>
        <Field label="实例端口"><Input aria-label={`第 ${i + 1} 条本机端口`} type="number" min={1} max={65535} step={1} placeholder="10001" value={m.local_port} onChange={(e) => patch(i, { local_port: e.target.value })} /></Field>
        <Field label="端口数量" hint="单个端口填 1；多个端口须连续"><Input aria-label={`第 ${i + 1} 条连续数量`} type="number" min={1} max={4096} step={1} value={m.count} onChange={(e) => patch(i, { count: e.target.value })} /></Field>
        <Field label="协议"><Select aria-label={`第 ${i + 1} 条传输`} value={m.network} onChange={(e) => patch(i, { network: e.target.value as NATDraft["mappings"][number]["network"] })}><option value="both">TCP + UDP</option><option value="tcp">TCP</option><option value="udp">UDP</option></Select></Field>
      </div>{(() => {
        const pub = Number(m.public_port), local = Number(m.local_port), count = Number(m.count);
        return [m.public_port, m.local_port, m.count].every((v) => /^\d+$/.test(v)) && pub > 0 && local > 0 && count > 0 && pub + count - 1 <= 65535 && local + count - 1 <= 65535
          ? <p className="mt-3 wrap-break-word text-xs text-muted-foreground">公网 {natPortRange(pub, count)} → 实例 {natPortRange(local, count)} · {m.network === "both" ? "TCP + UDP" : m.network.toUpperCase()}</p>
          : null;
      })()}</div>)}
      <Button size="sm" variant="outline" onClick={() => setDraft((p) => ({ ...p, mappings: [...p.mappings, emptyNATMapping()] }))}><Plus className="h-4 w-4" /> 添加映射</Button>
      <details className="rounded-lg border p-3"><summary className="cursor-pointer text-sm font-medium">高级设置：网卡和本机 IPv4</summary><div className="mt-3 grid gap-3 sm:grid-cols-2"><Field label="转入网卡" hint="留空适用全部网卡"><Input aria-label="NAT 转入网卡" maxLength={15} value={draft.interface} onChange={(e) => setDraft((p) => ({ ...p, interface: e.target.value }))} placeholder="eth0" /></Field><Field label="本机目的 IPv4" hint="留空按实际目的地址适配"><Input aria-label="NAT 本机目的 IPv4" value={draft.local_address} onChange={(e) => setDraft((p) => ({ ...p, local_address: e.target.value }))} placeholder="192.0.2.10" /></Field></div></details>
  </div>;
}
