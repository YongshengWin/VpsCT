import * as React from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, RefreshCw, Trash2, Pencil, Upload, ExternalLink } from "lucide-react";
import { del, get, post, put } from "@/lib/api";
import type { ExternalSubscription, Node, Series } from "@/lib/types";
import { fmtBytes, fmtAgo, fmtDate } from "@/lib/utils";
import { Badge, Button, Card, CardContent, CardHeader, CardTitle, Confirm, Dialog, Empty, Field, Input, PageHeader, Progress, Spinner, Switch, Table, Th, Textarea, Code } from "@/components/ui";
import { useToast } from "@/components/toast";
import { TrafficBars } from "@/components/charts";
import { NodeRow, NodeDetailDialog } from "@/pages/nodes";

interface Form { name: string; url: string; user_agent: string; sync_interval_min: number; enabled: boolean }
const empty: Form = { name: "", url: "", user_agent: "", sync_interval_min: 360, enabled: true };

function ExternalDialog({ open, onClose, ext }: { open: boolean; onClose: () => void; ext?: ExternalSubscription }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [f, setF] = React.useState<Form>(empty);
  React.useEffect(() => setF(ext ? { name: ext.name, url: ext.url, user_agent: ext.user_agent, sync_interval_min: ext.sync_interval_min, enabled: ext.enabled } : empty), [ext, open]);
  const set = <K extends keyof Form>(k: K, v: Form[K]) => setF((p) => ({ ...p, [k]: v }));
  const m = useMutation({
    mutationFn: async () => (ext ? put<ExternalSubscription>(`/api/v1/externals/${ext.id}`, f) : post<{ external: ExternalSubscription; sync_error: string }>("/api/v1/externals", f)),
    onSuccess: (r) => {
      qc.invalidateQueries({ queryKey: ["externals"] });
      qc.invalidateQueries({ queryKey: ["nodes"] });
      const syncErr = (r as { sync_error?: string }).sync_error;
      if (syncErr) toast.error("已添加，但首次同步失败", syncErr);
      else toast.success(ext ? "已保存" : "已添加并同步");
      onClose();
    },
    onError: (e) => toast.fromError(e),
  });
  return (
    <Dialog open={open} onClose={onClose} title={ext ? "编辑订阅源" : "导入外部订阅"} description="定时拉取机场/他人订阅，节点进入节点库并聚合流量信息（Subscription-Userinfo）。"
      footer={<><Button variant="outline" onClick={onClose}>取消</Button><Button onClick={() => m.mutate()} loading={m.isPending}>{ext ? "保存" : "添加并同步"}</Button></>}>
      <div className="grid gap-4">
        <Field label="名称"><Input value={f.name} onChange={(e) => set("name", e.target.value)} placeholder="机场 A" /></Field>
        <Field label="订阅地址"><Input value={f.url} onChange={(e) => set("url", e.target.value)} placeholder="https://..." /></Field>
        <div className="grid grid-cols-2 gap-3">
          <Field label="User-Agent" hint="留空用 clash.meta"><Input value={f.user_agent} onChange={(e) => set("user_agent", e.target.value)} placeholder="clash.meta" /></Field>
          <Field label="同步间隔（分钟）"><Input type="number" min={10} value={f.sync_interval_min} onChange={(e) => set("sync_interval_min", Number(e.target.value))} /></Field>
        </div>
        <Switch checked={f.enabled} onChange={(v) => set("enabled", v)} label="启用定时同步" />
      </div>
    </Dialog>
  );
}

function ImportBodyDialog({ ext, onClose }: { ext: ExternalSubscription | null; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [body, setBody] = React.useState("");
  const m = useMutation({
    mutationFn: () => post<{ stats: { added: number; updated: number; removed: number } }>(`/api/v1/externals/${ext!.id}/import-body`, { body }),
    onSuccess: (r) => { toast.success("已导入", `新增 ${r.stats.added} · 更新 ${r.stats.updated} · 移除 ${r.stats.removed}`); qc.invalidateQueries({ queryKey: ["externals"] }); qc.invalidateQueries({ queryKey: ["nodes"] }); setBody(""); onClose(); },
    onError: (e) => toast.fromError(e),
  });
  return (
    <Dialog open={!!ext} onClose={onClose} title={`手动导入到 ${ext?.name ?? ""}`} description="服务端无法直连订阅地址时，把本机下载到的 Clash YAML / base64 内容粘贴到这里。" size="md"
      footer={<><Button variant="outline" onClick={onClose}>取消</Button><Button onClick={() => m.mutate()} loading={m.isPending} disabled={!body.trim()}>导入</Button></>}>
      <Textarea rows={16} className="mono" value={body} onChange={(e) => setBody(e.target.value)} placeholder="proxies:\n  - name: ..." />
    </Dialog>
  );
}

export function ExternalsPage() {
  const qc = useQueryClient();
  const toast = useToast();
  const q = useQuery({ queryKey: ["externals"], queryFn: () => get<ExternalSubscription[]>("/api/v1/externals"), refetchInterval: 30000 });
  const [create, setCreate] = React.useState(false);
  const [edit, setEdit] = React.useState<ExternalSubscription | undefined>();
  const [importTo, setImportTo] = React.useState<ExternalSubscription | null>(null);
  const [confirmDel, setConfirmDel] = React.useState<ExternalSubscription | null>(null);
  const [expanded, setExpanded] = React.useState<number | null>(null);
  const sync = useMutation({
    mutationFn: (id: number) => post<{ stats: { added: number; updated: number; removed: number } }>(`/api/v1/externals/${id}/sync`),
    onSuccess: (r) => { toast.success("同步完成", `新增 ${r.stats.added} · 更新 ${r.stats.updated} · 移除 ${r.stats.removed}`); qc.invalidateQueries({ queryKey: ["externals"] }); qc.invalidateQueries({ queryKey: ["nodes"] }); },
    onError: (e) => toast.fromError(e, "同步失败"),
  });
  const delM = useMutation({
    mutationFn: (id: number) => del(`/api/v1/externals/${id}`),
    onSuccess: () => { toast.success("已删除"); setConfirmDel(null); qc.invalidateQueries({ queryKey: ["externals"] }); qc.invalidateQueries({ queryKey: ["nodes"] }); },
    onError: (e) => toast.fromError(e),
  });
  return (
    <div>
      <PageHeader title="订阅导入" description="从其他订阅 URL 拉取节点并跟踪其流量用量" actions={<Button onClick={() => setCreate(true)}><Plus className="h-4 w-4" /> 导入订阅</Button>} />
      {q.isLoading ? <Spinner /> : !q.data?.length ? (
        <Empty title="还没有外部订阅" description="粘贴机场订阅地址，节点会自动进入节点库，可用于生成订阅或分享。" action={<Button onClick={() => setCreate(true)}>导入订阅</Button>} />
      ) : (
        <div className="space-y-3">
          {q.data.map((e) => {
            const ui = e.userinfo;
            const used = ui ? ui.upload + ui.download : 0;
            const pct = ui && ui.total > 0 ? (used / ui.total) * 100 : 0;
            return (
              <Card key={e.id}>
                <div className="flex flex-col gap-3 p-4 sm:flex-row sm:items-center sm:justify-between">
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <button className="break-words text-left font-medium hover:underline" onClick={() => setExpanded(expanded === e.id ? null : e.id)}>{e.name}</button>
                      <Badge variant={e.enabled ? "success" : "secondary"}>{e.enabled ? "同步中" : "已停用"}</Badge>
                      <Badge variant="outline">{e.node_count} 节点</Badge>
                      {e.last_error && <Badge variant="destructive">同步失败</Badge>}
                    </div>
                    <p className="mt-1 break-all text-xs text-muted-foreground"><a href={e.url} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 hover:underline"><ExternalLink className="h-3 w-3" />{e.url}</a></p>
                    <p className="mt-1 text-xs text-muted-foreground">上次同步 {fmtAgo(e.last_sync_at)} · 每 {e.sync_interval_min} 分钟{e.last_error && <span className="text-red-500"> · {e.last_error}</span>}</p>
                  </div>
                  <div className="flex shrink-0 items-center gap-1">
                    <Button size="sm" variant="outline" onClick={() => sync.mutate(e.id)} loading={sync.isPending && sync.variables === e.id}><RefreshCw className="h-4 w-4" /> 同步</Button>
                    <Button size="sm" variant="ghost" title="手动导入内容" onClick={() => setImportTo(e)}><Upload className="h-4 w-4" /></Button>
                    <Button size="sm" variant="ghost" onClick={() => setEdit(e)}><Pencil className="h-4 w-4" /></Button>
                    <Button size="sm" variant="ghost" className="text-red-500" onClick={() => setConfirmDel(e)}><Trash2 className="h-4 w-4" /></Button>
                  </div>
                </div>
                {ui && (
                  <div className="border-t px-4 py-3">
                    <div className="mb-1 flex flex-wrap justify-between gap-2 text-xs text-muted-foreground">
                      <span>机场流量：汇总 {fmtBytes((ui.upload ?? 0) + (ui.download ?? 0))}{ui.total > 0 && ` · 机场额度 ${fmtBytes(used)} / ${fmtBytes(ui.total)}`}（上传 {fmtBytes(ui.upload)} · 下载 {fmtBytes(ui.download)}）</span>
                      <span>{ui.expire > 0 ? `到期 ${fmtDate(new Date(ui.expire * 1000).toISOString(), false)}` : "无到期时间"}</span>
                    </div>
                    {ui.total > 0 && <Progress value={pct} />}
                  </div>
                )}
                {expanded === e.id && <ExternalDetail ext={e} />}
              </Card>
            );
          })}
        </div>
      )}
      <ExternalDialog open={create} onClose={() => setCreate(false)} />
      <ExternalDialog open={!!edit} onClose={() => setEdit(undefined)} ext={edit} />
      <ImportBodyDialog ext={importTo} onClose={() => setImportTo(null)} />
      <Confirm open={!!confirmDel} onClose={() => setConfirmDel(null)} onConfirm={() => confirmDel && delM.mutate(confirmDel.id)} loading={delM.isPending} destructive title={`删除「${confirmDel?.name}」？`} description="该订阅导入的所有节点会一并删除，引用它们的订阅会自动去掉这些节点。" />
    </div>
  );
}

function ExternalDetail({ ext }: { ext: ExternalSubscription }) {
  const nodes = useQuery({ queryKey: ["nodes", { external_sub_id: ext.id }], queryFn: () => get<Node[]>(`/api/v1/nodes?external_sub_id=${ext.id}`) });
  const traffic = useQuery({ queryKey: ["externals", ext.id, "traffic"], queryFn: () => get<Series>(`/api/v1/externals/${ext.id}/traffic?days=30`) });
  const [detail, setDetail] = React.useState<Node | null>(null);
  return (
    <div className="grid gap-4 border-t p-4 lg:grid-cols-2">
      <Card>
        <CardHeader><CardTitle>近 30 天用量变化</CardTitle></CardHeader>
        <CardContent>
          {traffic.data?.points?.length ? <TrafficBars points={traffic.data.points} height={180} /> : <p className="text-sm text-muted-foreground">每次同步记录 Userinfo 差值；数据需要至少两次同步后出现。</p>}
        </CardContent>
      </Card>
      <div>
        <p className="mb-2 text-sm font-medium">节点（{nodes.data?.length ?? 0}）<span className="ml-2 text-xs text-muted-foreground">每次同步以订阅内容为准覆盖，手工修改会被还原；如需固定请用 <Code>手动</Code> 节点。</span></p>
        <div className="max-h-96 overflow-auto">
          {nodes.isLoading ? <Spinner /> : !nodes.data?.length ? <p className="text-sm text-muted-foreground">无节点</p> : (
            <Table>
              <thead><tr className="border-b"><Th>名称</Th><Th>协议</Th><Th>地址</Th><Th>来源</Th><Th>近 30 天用量</Th><Th>状态</Th><Th></Th></tr></thead>
              <tbody>{nodes.data.map((n) => <NodeRow key={n.id} n={n} onOpen={() => setDetail(n)} />)}</tbody>
            </Table>
          )}
        </div>
      </div>
      <NodeDetailDialog node={detail} onClose={() => setDetail(null)} />
    </div>
  );
}