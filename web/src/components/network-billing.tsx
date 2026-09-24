import * as React from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { get, put } from "@/lib/api";
import type { NetworkBillingView, NetworkView, Server } from "@/lib/types";
import { fmtAgo } from "@/lib/utils";
import { Badge, Button, Card, CardContent, CardHeader, CardTitle, Confirm, Field, Select, Spinner, Switch } from "@/components/ui";
import { useToast } from "@/components/toast";

export function NetworkBilling({ server, inventory }: { server: Server; inventory: NetworkView }) {
  const qc = useQueryClient();
  const toast = useToast();
  const q = useQuery({ queryKey: ["network-billing", server.id], queryFn: () => get<NetworkBillingView>(`/api/v1/servers/${server.id}/network/billing`), refetchInterval: 10000 });
  const [editing, setEditing] = React.useState(false);
  const [mode, setMode] = React.useState<"legacy" | "interfaces">("interfaces");
  const [ids, setIDs] = React.useState<string[]>([]);
  const [confirmed, setConfirmed] = React.useState(false);
  const [review, setReview] = React.useState(false);
  const latest = q.data?.requested;
  React.useEffect(() => {
    if (latest) { setMode(latest.mode); setIDs(latest.interface_ids ?? []); setConfirmed(false); setReview(false); }
  }, [server.id, latest?.revision]);
  const m = useMutation({ mutationFn: () => put<NetworkBillingView>(`/api/v1/servers/${server.id}/network/billing`, { expected_revision: latest?.revision ?? 0, mode, interface_ids: mode === "interfaces" ? ids : [], boundary_confirmed: confirmed }),
    onSuccess: (data) => { qc.setQueryData(["network-billing", server.id], data); setEditing(false); setReview(false); toast.success("已申请切换，等待 agent 结算并确认"); }, onError: (e) => toast.fromError(e) });
  if (q.isPending) return <Spinner />;
  if (q.isError) return <p className="text-sm text-destructive">计费来源加载失败：{q.error.message}</p>;
  const names = new Map(inventory.interfaces.map((n) => [n.interface.id, `${n.interface.name}${n.present ? "" : "（已消失）"}`]));
  const describe = (value: "legacy" | "interfaces", keys: string[] | null) => value === "legacy" ? `原有自动统计${server.metrics?.interface ? `（${server.metrics.interface}）` : ""}` : (keys ?? []).map((id) => names.get(id) ?? "未知 / 已消失网卡").join("、");
  const pending = q.data.current.revision !== q.data.requested.revision;
  const capable = (server.diagnostics?.network_billing_version ?? 0) >= 1;
  const candidates = inventory.interfaces.filter((n) => n.present && n.interface.kind !== "loopback");
  const missing = ids.filter((id) => !candidates.some((n) => n.interface.id === id));
  const toggle = (id: string, checked: boolean) => setIDs((old) => checked ? [...old.filter((v) => v !== id), id] : old.filter((v) => v !== id));
  return <Card>
    <CardHeader><CardTitle>流量配额按什么计算？</CardTitle></CardHeader>
    <CardContent className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2"><span>当前来源：{describe(q.data.current.mode, q.data.current.interface_ids)}</span><Badge variant={pending || q.data.status === "switching" || q.data.status === "incomplete" ? "warning" : "success"}>{pending ? "等待切换" : q.data.status === "switching" ? "等待确认" : q.data.status === "incomplete" ? "计量不完整" : "已生效"}</Badge></div>
      {pending && <p className="text-sm">待生效：{describe(q.data.requested.mode, q.data.requested.interface_ids)}。切换快照会持久保存，确认后继续累计；节点和分享计量保持运行。</p>}
      {q.data.error && <p role="status" className="text-sm text-amber-700 dark:text-amber-300">{q.data.error}</p>}
      {server.diagnostics?.network_billing_error && <p className="text-sm text-amber-700 dark:text-amber-300">{server.diagnostics.network_billing_error}</p>}
      <p className="text-sm text-muted-foreground">配额按当前来源的接收量 + 发出量统计。更换来源不会改变节点从哪里上网。</p>
      <details className="text-xs text-muted-foreground"><summary className="cursor-pointer">计费切换如何生效？</summary><p className="mt-2">更换来源会先结算旧集合，再从同一采样边界统计新集合；不重算既有账单。{q.data.applied_at ? `最近切换：${fmtAgo(q.data.applied_at)}。` : ""}</p></details>
      {!capable ? <p className="text-sm text-muted-foreground">当前 agent 尚不支持切换计费来源，请先升级。</p> : <Button variant="outline" onClick={() => setEditing(!editing)}>{editing ? "收起" : "更改计费来源"}</Button>}
      {editing && capable && <div className="space-y-4 rounded-lg border p-4">
        <Field label="统计方式"><Select aria-label="网卡计费方式" value={mode} onChange={(e) => { setMode(e.target.value as "legacy" | "interfaces"); setConfirmed(false); }}><option value="interfaces">指定计费网卡</option><option value="legacy">恢复原有自动统计</option></Select></Field>
        {mode === "interfaces" && <div className="space-y-2">
          {candidates.map(({ interface: n }) => <Switch key={n.id} checked={ids.includes(n.id)} onChange={(checked) => { toggle(n.id, checked); setConfirmed(false); }} label={`${n.name} · ${n.kind}${n.counters_valid ? "" : "（计数不可用）"}`} />)}
          {missing.map((id) => <Switch key={id} checked onChange={() => { toggle(id, false); setConfirmed(false); }} label={`${names.get(id) ?? "未知接口"}（不可用于新策略，请取消选择）`} />)}
          {inventory.snapshot?.status !== "ok" && <p className="text-sm text-amber-700 dark:text-amber-300">等待完整网卡清单后才能指定新的计费网卡。</p>}
          <p className="text-xs text-muted-foreground">不要同时选择网桥与成员、VLAN 与父接口、隧道与承载接口。软件会拒绝已知重复关系，复杂拓扑需要核对运营商的计量边界。</p>
        </div>}
        <Switch checked={confirmed} onChange={setConfirmed} label="我已确认所选来源对应同一个计量层，并按入站加出站统计" />
        <Button onClick={() => setReview(true)} disabled={!confirmed || (mode === "interfaces" && (!ids.length || ids.length > 16 || inventory.snapshot?.status !== "ok"))}>预览切换</Button>
      </div>}
      <Confirm open={review} onClose={() => setReview(false)} title="切换服务器计费来源" description={`当前：${describe(q.data.current.mode, q.data.current.interface_ids)}。目标：${describe(mode, ids)}。切换在 agent 的采样边界生效，先结算旧来源，已有用量不会清零。接口丢失时仅计入已知增量，不自动换用其他网卡。`} loading={m.isPending} onConfirm={() => m.mutate()} />
    </CardContent>
  </Card>;
}
