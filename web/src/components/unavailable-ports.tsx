import * as React from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { get, put, del } from "@/lib/api";
import { fmtDate } from "@/lib/utils";
import { portTransportLabels, type UnavailablePort } from "@/lib/unavailable-ports";
import { Badge, Button, Confirm, Field, Input, Select, Spinner, Textarea } from "@/components/ui";
import { useToast } from "@/components/toast";

export function UnavailablePortWarning({ record }: { record?: UnavailablePort }) {
  if (!record) return null;
  return <div role="note" className="space-y-1 rounded-lg border border-amber-500/40 bg-amber-500/5 p-3 text-sm text-amber-800 dark:text-amber-300">
    <p className="font-medium">端口 {record.port} 已有不可用记录 · {portTransportLabels[record.transport]}</p>
    {record.network && <p>网络／地区：{record.network}</p>}{record.reason && <p className="whitespace-pre-wrap break-words">原因：{record.reason}</p>}
    <p className="text-xs">这是人工记录，不代表所有网络都不可用。请核对适用网络，必要时选择其他端口。</p>
  </div>;
}

export function UnavailablePorts({ serverID, currentPort }: { serverID: number; currentPort?: number }) {
  const qc = useQueryClient(), toast = useToast();
  const [editing, setEditing] = React.useState(false);
  const [port, setPort] = React.useState(String(currentPort ?? ""));
  const [transport, setTransport] = React.useState<UnavailablePort["transport"]>("both");
  const [network, setNetwork] = React.useState("");
  const [reason, setReason] = React.useState("");
  const [removing, setRemoving] = React.useState<UnavailablePort | null>(null);
  const records = useQuery({ queryKey: ["unavailable-ports", serverID], queryFn: () => get<UnavailablePort[]>(`/api/v1/servers/${serverID}/unavailable-ports`), refetchInterval: 10000 });
  const refresh = () => qc.invalidateQueries({ queryKey: ["unavailable-ports", serverID] });
  const save = useMutation({ mutationFn: () => put<UnavailablePort>(`/api/v1/servers/${serverID}/unavailable-ports/${Number(port)}`, { transport, network, reason }), onSuccess: () => { void refresh(); setEditing(false); toast.success("不可用端口记录已保存"); }, onError: e => toast.fromError(e) });
  const remove = useMutation({ mutationFn: (p: UnavailablePort) => del(`/api/v1/servers/${serverID}/unavailable-ports/${p.port}`), onSuccess: () => { void refresh(); setRemoving(null); toast.success("已移除记录，未修改节点"); }, onError: e => toast.fromError(e) });
  const edit = (p?: UnavailablePort) => { setPort(String(p?.port ?? currentPort ?? "")); setTransport(p?.transport ?? "both"); setNetwork(p?.network ?? ""); setReason(p?.reason ?? ""); setEditing(true); };
  const existing = records.data?.some(p => p.port === Number(port));
  const valid = /^\d+$/.test(port) && Number(port) >= 1 && Number(port) <= 65535 && new TextEncoder().encode(network.trim()).length <= 120 && new TextEncoder().encode(reason.trim()).length <= 500;
  return <section className="mt-4 space-y-3 rounded-xl border p-4" aria-label="不可用端口记录">
    <div className="flex flex-wrap items-center justify-between gap-2"><h3 className="font-semibold">不可用端口记录</h3>{!editing && <Button size="sm" variant="outline" onClick={() => edit()}>记录不可用端口</Button>}</div>
    <p className="text-xs text-muted-foreground">按服务器保存人工记录，可注明本地网络或地区。记录不触发探测、停用节点或换端口；换端口预览会提醒已有记录。</p>
    {records.isPending ? <Spinner /> : records.isError ? <p role="alert" className="text-sm text-destructive">记录读取失败：{records.error.message}</p> : records.data.length === 0 ? <p className="text-sm text-muted-foreground">暂无记录</p> : <ul className="max-h-72 space-y-2 overflow-y-auto">{records.data.map(p => <li key={p.port} className="space-y-2 rounded-lg bg-muted/40 p-3 text-sm">
      <div className="flex flex-wrap items-center gap-2"><span className="font-mono font-semibold">{p.port}</span><Badge variant="outline">{portTransportLabels[p.transport]}</Badge>{p.port === currentPort && <Badge variant="warning">当前节点端口</Badge>}<span className="text-xs text-muted-foreground">{fmtDate(p.updated_at)}</span></div>
      {p.network && <p className="break-words">网络／地区：{p.network}</p>}{p.reason && <p className="whitespace-pre-wrap break-words text-muted-foreground">{p.reason}</p>}
      <div className="flex gap-2"><Button size="sm" variant="link" disabled={save.isPending} onClick={() => edit(p)}>编辑记录</Button><Button size="sm" variant="link" onClick={() => setRemoving(p)}>移除记录</Button></div>
    </li>)}</ul>}
    {editing && <fieldset disabled={save.isPending} className="space-y-3 rounded-lg border p-3">
      <div className="grid gap-3 sm:grid-cols-2"><Field label="不可用端口"><Input aria-label="不可用端口" type="number" min={1} max={65535} value={port} onChange={e => setPort(e.target.value)} /></Field><Field label="受影响传输"><Select aria-label="受影响传输" value={transport} onChange={e => setTransport(e.target.value as UnavailablePort["transport"])}><option value="both">TCP + UDP</option><option value="tcp">TCP</option><option value="udp">UDP</option></Select></Field></div>
      <Field label="网络／地区（可选）" hint="例如：本地电信、某地区移动；不必填写本地 IP。"><Input aria-label="网络或地区" value={network} maxLength={120} onChange={e => setNetwork(e.target.value)} /></Field>
      <Field label="原因（可选）"><Textarea aria-label="不可用原因" value={reason} maxLength={500} rows={2} onChange={e => setReason(e.target.value)} /></Field>
      {existing && <p className="text-xs text-amber-700 dark:text-amber-300">该端口已有记录，本次保存会更新其传输、网络和原因。</p>}
      {!valid && <p className="text-xs text-destructive">请填写 1–65535 的端口；网络最多 120 字节，原因最多 500 字节。</p>}
      <div className="flex justify-end gap-2"><Button size="sm" variant="outline" onClick={() => setEditing(false)}>取消</Button><Button size="sm" disabled={!valid} loading={save.isPending} onClick={() => save.mutate()}>保存记录</Button></div>
    </fieldset>}
    <Confirm open={!!removing} onClose={() => { if (!remove.isPending) setRemoving(null); }} title={`移除端口 ${removing?.port} 的记录`} description="仅移除人工记录，不会修改节点或网络配置。" onConfirm={() => { if (removing) remove.mutate(removing); }} loading={remove.isPending} />
  </section>;
}
