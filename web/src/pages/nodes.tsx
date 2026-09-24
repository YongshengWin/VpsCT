import { SSHClientFields } from "@/components/ssh-client-fields";
import { TunnelClientFields } from "@/components/tunnel-client-fields";
import * as React from "react";
import { Link } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Copy, Plus, Upload, Trash2, Pencil, QrCode, RotateCcw, Search, GripVertical, Link2, Unlink, ArrowRightLeft } from "lucide-react";
import { DndContext, closestCenter, type DragEndEvent, PointerSensor, useSensor, useSensors } from "@dnd-kit/core";
import { SortableContext, arrayMove, useSortable, verticalListSortingStrategy } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { del, get, post, put } from "@/lib/api";
import type { Node, Series } from "@/lib/types";
import { copyText, fmtBytes, fmtDate, PROTOCOL_LABELS, cn } from "@/lib/utils";
import { Badge, Button, Confirm, Dialog, Empty, Field, Input, PageHeader, Select, Spinner, Switch, Table, Td, Th, Tr, Textarea, Pre, Tabs, Code } from "@/components/ui";
import { useToast } from "@/components/toast";
import { TrafficIO } from "@/components/traffic-ways";
import { TrafficBars } from "@/components/charts";
import { NodeNetwork } from "@/components/node-network";
import { useIsAdmin } from "@/lib/auth";

export function ProtocolBadge({ p }: { p: string }) {
  return <Badge variant="outline">{PROTOCOL_LABELS[p] ?? p}</Badge>;
}

export function SourceBadge({ n }: { n: Node }) {
  if (n.share_id) return <Badge variant="info">分享 · {n.share_name}</Badge>;
  if (n.source === "deployed") return <Badge variant="success">部署 · {n.server_name}</Badge>;
  if (n.source === "imported") return <Badge variant="secondary">导入 · {n.external_name}</Badge>;
  if (n.source === "chain") return <Badge variant="info">链式{n.chain_front_name ? ` · 经 ${n.chain_front_name}` : ""}</Badge>;
  return <Badge variant="secondary">手动</Badge>;
}

export function NodeRow({ n, onOpen, selectable, selected, onSelect, dragHandle, rowRef, style, configurationHint }: { configurationHint?: string; n: Node; onOpen: () => void; selectable?: boolean; selected?: boolean; onSelect?: (v: boolean) => void; dragHandle?: React.ReactNode; rowRef?: (el: HTMLTableRowElement | null) => void; style?: React.CSSProperties }) {
  return (
    <Tr ref={rowRef} style={style} className={cn(n.revoked && "opacity-50", "cursor-pointer")} onClick={onOpen}>
      {selectable && (
        <Td className="w-8" onClick={(e) => e.stopPropagation()}>
          <input type="checkbox" checked={!!selected} onChange={(e) => onSelect?.(e.target.checked)} className="h-4 w-4 accent-primary" />
        </Td>
      )}
      {dragHandle !== undefined && <Td className="w-8" onClick={(e) => e.stopPropagation()}>{dragHandle}</Td>}
      <Td>
        <p className="max-w-[20rem] break-words font-medium">{n.name}</p>
        {n.source === "chain" && n.chain_front_name && <p className="mt-0.5 text-[11px] text-muted-foreground">经 {n.chain_front_name} 前置</p>}
        {n.tags?.length ? <p className="mt-0.5 flex flex-wrap gap-1">{n.tags.map((t) => <span key={t} className="rounded bg-muted px-1 text-[10px] text-muted-foreground">{t}</span>)}</p> : null}
      </Td>
      <Td><ProtocolBadge p={n.protocol} /></Td>
      <Td className="mono text-xs text-muted-foreground">{n.server || "—"}:{n.port}</Td>
      <Td><SourceBadge n={n} /></Td>
      <Td>{n.source !== "deployed" ? <span className="text-xs text-muted-foreground">不可采集</span> : !n.traffic ? <span className="text-xs text-muted-foreground">暂不可用</span> : !n.traffic.has_data ? <span className="text-xs text-muted-foreground">暂无采集记录</span> : <div><p className="tabular-nums font-medium">{fmtBytes(n.traffic.total)}</p><p className="text-xs text-muted-foreground">入 {fmtBytes(n.traffic.inbound)} · 出 {fmtBytes(n.traffic.outbound)}</p></div>}</Td>
      <Td>{configurationHint && <p className="mb-1 max-w-48 text-xs text-amber-600">{configurationHint} · 见服务器提示</p>}{n.revoked ? <Badge variant="destructive">已撤销</Badge> : n.enabled ? <Badge variant="success">启用</Badge> : <Badge variant="secondary">禁用</Badge>}</Td>
      <Td className="text-right" onClick={(e) => e.stopPropagation()}>
        {n.uri && (
          <Button size="icon" variant="ghost" title="复制链接" onClick={async () => { await copyText(n.uri!); }}>
            <Copy className="h-4 w-4" />
          </Button>
        )}
      </Td>
    </Tr>
  );
}

function SortableRow({ n, onOpen, selected, onSelect }: { n: Node; onOpen: () => void; selected: boolean; onSelect: (v: boolean) => void }) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({ id: n.id });
  const style = { transform: CSS.Transform.toString(transform), transition, opacity: isDragging ? 0.6 : 1 } as React.CSSProperties;
  return (
    <NodeRow
      n={n}
      onOpen={onOpen}
      selectable
      selected={selected}
      onSelect={onSelect}
      rowRef={setNodeRef}
      style={style}
      dragHandle={<button {...attributes} {...listeners} className="cursor-grab touch-none text-muted-foreground hover:text-foreground"><GripVertical className="h-4 w-4" /></button>}
    />
  );
}

export function NodesPage() {
  const qc = useQueryClient();
  const toast = useToast();
  const [source, setSource] = React.useState<"" | Node["source"]>("");
  const [q, setQ] = React.useState("");
 const [serverFilter,setServerFilter]=React.useState("");
 const [sortUsage,setSortUsage]=React.useState(false);
  const [showRevoked, setShowRevoked] = React.useState(false);
  const nodes = useQuery({ queryKey: ["nodes", { source, showRevoked }], queryFn: () => get<Node[]>(`/api/v1/nodes?source=${source}${showRevoked ? "&include_revoked=1" : ""}`) });
  const [order, setOrder] = React.useState<Node[]>([]);
  React.useEffect(() => setOrder(nodes.data ?? []), [nodes.data]);
  const [selected, setSelected] = React.useState<Set<number>>(new Set());
  const [importOpen, setImportOpen] = React.useState(false);
  const [createOpen, setCreateOpen] = React.useState(false);
  const [detail, setDetail] = React.useState<Node | null>(null);
  const [confirmBulk, setConfirmBulk] = React.useState(false);
  const [chainOpen, setChainOpen] = React.useState(false);
  const [resetNodes, setResetNodes] = React.useState<Node[] | null>(null);
  const resettable = order.filter((n) => selected.has(n.id) && n.source === "deployed" && n.server_id && !n.revoked);

  const sensors = useSensors(useSensor(PointerSensor, { activationConstraint: { distance: 6 } }));
  const reorder = useMutation({ mutationFn: (ids: number[]) => post("/api/v1/nodes/reorder", { ids }), onError: (e) => toast.fromError(e) });
  const onDragEnd = (e: DragEndEvent) => {
    const { active, over } = e;
    if (!over || active.id === over.id) return;
    const from = order.findIndex((n) => n.id === active.id);
    const to = order.findIndex((n) => n.id === over.id);
    const next = arrayMove(order, from, to);
    setOrder(next);
    reorder.mutate(next.map((n) => n.id));
  };
  const bulkDel = useMutation({
    mutationFn: () => post<{ deleted: number }>("/api/v1/nodes/bulk-delete", { ids: [...selected] }),
    onSuccess: (r) => { toast.success(`已删除 ${r.deleted} 个节点`); setSelected(new Set()); setConfirmBulk(false); qc.invalidateQueries({ queryKey: ["nodes"] }); },
    onError: (e) => toast.fromError(e),
  });

  const filtered = order.filter((n) => (!serverFilter || String(n.server_id)===serverFilter) && (!q || n.name.toLowerCase().includes(q.toLowerCase()) || n.server.includes(q) || n.protocol.includes(q)));
 if(sortUsage)filtered.sort((a,b)=>(b.traffic?.total??-1)-(a.traffic?.total??-1));
 const serverOptions=[...new Map(order.filter(n=>n.server_id).map(n=>[n.server_id,n.server_name])).entries()];
  const canDrag = !q && source !== "" && !serverFilter && !sortUsage;

  return (
    <div>
      <PageHeader
        title="节点"
        description="手动导入的个人/机场节点，以及各服务器上部署的节点"
        actions={
          <>
            <Button variant="outline" onClick={() => setImportOpen(true)}><Upload className="h-4 w-4" /> 批量导入</Button>
            <Button onClick={() => setCreateOpen(true)}><Plus className="h-4 w-4" /> 添加节点</Button>
          </>
        }
      />
      <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
        <Tabs className="shrink-0" value={source} onChange={(v) => { setSource(v); setSelected(new Set()); }} items={[{ value: "", label: "全部" }, { value: "manual", label: "手动" }, { value: "imported", label: "订阅导入" }, { value: "deployed", label: "已部署" }, { value: "chain", label: "链式" }]} />
        <div className="flex w-full flex-wrap items-center gap-3 xl:w-auto">
          <div className="relative w-full sm:w-56 sm:shrink-0">
            <Search className="absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
            <Input className="w-full pl-8" placeholder="搜索名称 / 地址 / 协议" value={q} onChange={(e) => setQ(e.target.value)} />
          </div>
          <div className="w-full sm:w-44 sm:shrink-0">
            <Select aria-label="按服务器筛选" value={serverFilter} onChange={(e) => setServerFilter(e.target.value)}>
              <option value="">全部服务器</option>
              {serverOptions.map(([id, name]) => <option key={id} value={String(id)}>{name}</option>)}
            </Select>
          </div>
          <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
            <div className="shrink-0 whitespace-nowrap"><Switch checked={sortUsage} onChange={setSortUsage} label="按用量排序" /></div>
            <div className="shrink-0 whitespace-nowrap"><Switch checked={showRevoked} onChange={setShowRevoked} label="含已撤销" /></div>
          </div>
        </div>
      </div>
      {selected.size > 0 && (
        <div className="mb-3 flex flex-wrap items-center gap-3 rounded-md border bg-muted/40 px-3 py-2 text-sm">
          已选 {selected.size} 个
          {selected.size === 2 && <Button size="sm" variant="outline" onClick={() => setChainOpen(true)}><Link2 className="h-4 w-4" /> 设为链式</Button>}
          {selected.size === 1 && [...selected].some((id) => order.find((n) => n.id === id)?.source === "chain") && (
            <Button size="sm" variant="outline" onClick={() => setChainOpen(true)}><Unlink className="h-4 w-4" /> 解除链式</Button>
          )}
          <Button size="sm" variant="outline" disabled={resettable.length === 0 || selected.size > 500} onClick={() => setResetNodes(order.filter((n) => selected.has(n.id)))}><RotateCcw className="h-4 w-4" /> 批量重置凭据{resettable.length > 0 ? `（${resettable.length}）` : ""}</Button>
          {resettable.length < selected.size && <span className="text-muted-foreground">仅支持未撤销的已部署节点</span>}
          {selected.size > 500 && <span className="text-muted-foreground">每次最多选择 500 个节点</span>}
          <Button size="sm" variant="destructive" onClick={() => setConfirmBulk(true)}><Trash2 className="h-4 w-4" /> 删除</Button>
          <Button size="sm" variant="ghost" onClick={() => setSelected(new Set())}>取消选择</Button>
        </div>
      )}
      {nodes.isLoading ? (
        <Spinner />
      ) : filtered.length === 0 ? (
        <Empty title="没有节点" description="粘贴 ss:// vless:// hysteria2:// 等链接、base64 订阅或 Clash YAML 批量导入。" action={<Button onClick={() => setImportOpen(true)}>批量导入</Button>} />
      ) : (
        <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={onDragEnd}>
          <SortableContext items={filtered.map((n) => n.id)} strategy={verticalListSortingStrategy}>
            <Table>
              <thead>
                <tr className="border-b">
                  <Th className="w-8"><input type="checkbox" className="h-4 w-4 accent-primary" checked={selected.size > 0 && selected.size === filtered.length} onChange={(e) => setSelected(e.target.checked ? new Set(filtered.map((n) => n.id)) : new Set())} /></Th>
                  <Th className="w-8"></Th>
                  <Th>名称</Th><Th>协议</Th><Th>地址</Th><Th>来源</Th><Th>近 30 天用量</Th><Th>状态</Th><Th></Th>
                </tr>
              </thead>
              <tbody>
                {filtered.map((n) =>
                  canDrag ? (
                    <SortableRow key={n.id} n={n} onOpen={() => setDetail(n)} selected={selected.has(n.id)} onSelect={(v) => setSelected((s) => { const c = new Set(s); v ? c.add(n.id) : c.delete(n.id); return c; })} />
                  ) : (
                    <NodeRow key={n.id} n={n} onOpen={() => setDetail(n)} selectable selected={selected.has(n.id)} onSelect={(v) => setSelected((s) => { const c = new Set(s); v ? c.add(n.id) : c.delete(n.id); return c; })} dragHandle={<span className="text-muted-foreground/30"><GripVertical className="h-4 w-4" /></span>} />
                  ),
                )}
              </tbody>
            </Table>
          </SortableContext>
        </DndContext>
      )}
      {canDrag && <p className="mt-2 text-xs text-muted-foreground">拖动左侧手柄可调整订阅中的节点顺序</p>}
      <ImportDialog open={importOpen} onClose={() => setImportOpen(false)} />
      <NodeEditDialog open={createOpen} onClose={() => setCreateOpen(false)} />
      <NodeDetailDialog node={detail} onClose={() => setDetail(null)} />
      <Confirm open={confirmBulk} onClose={() => setConfirmBulk(false)} onConfirm={() => bulkDel.mutate()} loading={bulkDel.isPending} destructive title={`删除 ${selected.size} 个节点？`} description="分享节点不会被删除；已部署节点会从服务器上撤下。" />
      {resetNodes && <BulkResetDialog nodes={resetNodes} onClose={() => setResetNodes(null)} onDone={() => setSelected(new Set())} />}
      <ChainDialog
        open={chainOpen}
        onClose={() => setChainOpen(false)}
        nodes={order}
        selectedIds={[...selected]}
        onDone={() => setSelected(new Set())}
      />
    </div>
  );
}

type BulkResetResponse = {
  rotated: number;
  results: { id: number; name: string; status: "rotated" | "skipped" | "failed"; message: string }[];
  servers: { id: number; name: string; published: boolean; revision: number }[];
};

function BulkResetDialog({ nodes, onClose, onDone }: { nodes: Node[]; onClose: () => void; onDone: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [result, setResult] = React.useState<BulkResetResponse | null>(null);
  const eligible = nodes.filter((n) => n.source === "deployed" && n.server_id && !n.revoked);
  const reset = useMutation({
    mutationFn: () => post<BulkResetResponse>("/api/v1/nodes/bulk-regenerate", { ids: nodes.map((n) => n.id) }),
    onSuccess: (data) => {
      setResult(data);
      qc.invalidateQueries({ queryKey: ["nodes"] });
      qc.invalidateQueries({ queryKey: ["subscriptions"] });
      qc.invalidateQueries({ queryKey: ["servers"] });
      qc.invalidateQueries({ queryKey: ["shares"] });
      onDone();
    },
    onError: (e) => toast.fromError(e),
  });
  const retry = useMutation({
    mutationFn: (id: number) => post(`/api/v1/servers/${id}/republish`),
    onSuccess: (_data, id) => {
      setResult((current) => current && ({ ...current, servers: current.servers.map((s) => s.id === id ? { ...s, published: true } : s) }));
      qc.invalidateQueries({ queryKey: ["servers"] });
    },
    onError: (e) => toast.fromError(e),
  });
  return <Dialog open onClose={() => { if (!reset.isPending && !retry.isPending) onClose(); }} size="md"
    title={result ? "批量重置结果" : `重置 ${eligible.length} 个节点的凭据？`}
    footer={result ? <Button onClick={onClose} disabled={retry.isPending}>完成</Button> : <>
      <Button variant="outline" onClick={onClose} disabled={reset.isPending}>取消</Button>
      <Button variant="destructive" loading={reset.isPending} disabled={eligible.length === 0} onClick={() => reset.mutate()}>确认重置</Button>
    </>}>
    {result ? <div className="space-y-4 text-sm">
      <p>已更新 {result.rotated} 个，跳过 {result.results.filter((n) => n.status === "skipped").length} 个，失败 {result.results.filter((n) => n.status === "failed").length} 个。</p>
      <p className="text-muted-foreground">订阅链接不变，请客户端更新订阅。旧凭据要等服务器应用配置后才失效；离线服务器恢复连接后才能应用。可进入服务器详情查看应用状态。</p>
      {result.servers.map((server) => <div key={server.id} className="flex flex-wrap items-center justify-between gap-2 rounded-lg border p-3">
        <div><Link className="font-medium underline" to={`/servers/${server.id}`}>{server.name}</Link><p className={server.published ? "text-muted-foreground" : "text-destructive"}>{server.published ? "配置已提交，待确认应用" : "配置提交失败，新凭据已保存；请重试下发"}</p></div>
        {!server.published && <Button size="sm" variant="outline" loading={retry.isPending && retry.variables === server.id} disabled={retry.isPending} onClick={() => retry.mutate(server.id)}>重试下发</Button>}
      </div>)}
      <ul className="space-y-2">{result.results.map((node) => <li key={node.id}><span className="font-medium">{node.name || `节点 #${node.id}`}</span>：{node.message}</li>)}</ul>
    </div> : <div className="space-y-3 text-sm">
      <p>重新生成所选节点的 UUID、密码等凭据，保留节点名称、端口和订阅链接。所有使用这些节点的客户端都需要更新订阅。</p>
      <p className="text-muted-foreground">服务器应用配置后旧凭据才会失效。如果订阅链接也已泄露，还需要更换订阅链接。</p>
      {eligible.length < nodes.length && <p>将跳过 {nodes.length - eligible.length} 个不支持的节点。链式节点请重置其前置或落地原始节点。</p>}
      <ul className="list-inside list-disc">{eligible.map((node) => <li key={node.id}>{node.name}</li>)}</ul>
    </div>}
  </Dialog>;
}

function ChainDialog({ open, onClose, nodes, selectedIds, onDone }: { open: boolean; onClose: () => void; nodes: Node[]; selectedIds: number[]; onDone: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const picked = selectedIds.map((id) => nodes.find((n) => n.id === id)).filter((n): n is Node => !!n);
  const [frontId, setFrontId] = React.useState(0);
  const [landingId, setLandingId] = React.useState(0);
  const [name, setName] = React.useState<string | null>(null);
  React.useEffect(() => {
    if (!open) return;
    setName(null);
    if (picked.length === 2) {
      setFrontId(picked[0].id);
      setLandingId(picked[1].id);
      return;
    }
    if (picked.length === 1 && picked[0].source === "chain") {
      setFrontId(picked[0].chain_front_node_id ?? 0);
      setLandingId(picked[0].id);
      return;
    }
    setFrontId(0);
    setLandingId(0);
  }, [open, selectedIds.join(","), nodes]);
  const front = nodes.find((n) => n.id === frontId);
  const landing = nodes.find((n) => n.id === landingId);
  const chainName = name ?? `${front?.name ?? "前置"} → ${landing?.name ?? "落地"}`;
  const save = useMutation({
    mutationFn: () => post<Node>("/api/v1/nodes/chain", { front_id: frontId, landing_id: landingId, name: chainName.trim() }),
    onSuccess: (node) => {
      toast.success(`已保存链式节点 ${node.name}`);
      qc.invalidateQueries({ queryKey: ["nodes"] });
      qc.invalidateQueries({ queryKey: ["subscriptions"] });
      onDone();
      onClose();
    },
    onError: (e) => toast.fromError(e),
  });
  const clear = useMutation({
    mutationFn: () => post<Node>("/api/v1/nodes/chain", { front_id: 0, landing_id: landingId || picked[0]?.id || 0 }),
    onSuccess: () => {
      toast.success("已解除链式");
      qc.invalidateQueries({ queryKey: ["nodes"] });
      onDone();
      onClose();
    },
    onError: (e) => toast.fromError(e),
  });
  const swap = () => { setFrontId(landingId); setLandingId(frontId); };
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="快捷链式"
      description="会新增一个独立节点，例如 Vmiss → Zouter。原来的两个节点不变。"
      footer={
        <>
          {(landing?.source === "chain" || picked[0]?.source === "chain") && (
            <Button variant="ghost" className="text-red-500" onClick={() => clear.mutate()} loading={clear.isPending}><Unlink className="h-4 w-4" /> 解除</Button>
          )}
          <Button variant="outline" onClick={onClose}>取消</Button>
          <Button onClick={() => save.mutate()} loading={save.isPending} disabled={!frontId || !landingId || frontId === landingId || !chainName.trim()}>保存</Button>
        </>
      }
    >
      <div className="grid gap-3 sm:grid-cols-[1fr_auto_1fr] sm:items-end">
        <Field label="前置">
          <Select value={String(frontId)} onChange={(e) => setFrontId(Number(e.target.value))}>
            <option value="0">选择节点…</option>
            {nodes.filter((n) => !n.revoked && n.source !== "chain" && n.id !== landingId).map((n) => <option key={n.id} value={n.id}>{n.name}</option>)}
          </Select>
        </Field>
        <Button variant="outline" size="icon" className="mb-0.5" title="对调" onClick={swap} disabled={!frontId || !landingId}><ArrowRightLeft className="h-4 w-4" /></Button>
        <Field label="落地">
          <Select value={String(landingId)} onChange={(e) => setLandingId(Number(e.target.value))}>
            <option value="0">选择节点…</option>
            {nodes.filter((n) => !n.revoked && n.source !== "chain" && n.id !== frontId).map((n) => <option key={n.id} value={n.id}>{n.name}</option>)}
          </Select>
        </Field>
      </div>
      <div className="mt-3">
        <Field label="链式节点名称">
          <Input aria-label="链式节点名称" value={chainName} onChange={(e) => setName(e.target.value)} />
        </Field>
      </div>
      {front && landing && (
        <p className="mt-3 text-sm text-muted-foreground">将新增 <span className="font-medium text-foreground">{chainName.trim()}</span>：本机 → {front.name} → {landing.name}</p>
      )}
    </Dialog>
  );
}

function ImportDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [text, setText] = React.useState("");
  const [tags, setTags] = React.useState("");
  const [preview, setPreview] = React.useState<{ format: string; proxies: Record<string, unknown>[]; errors: string[] } | null>(null);
  const parse = useMutation({ mutationFn: () => post<{ format: string; proxies: Record<string, unknown>[]; errors: string[] }>("/api/v1/nodes/parse", { text }), onSuccess: setPreview, onError: (e) => toast.fromError(e) });
  const imp = useMutation({
    mutationFn: () => post<{ created: Node[]; errors: string[] }>("/api/v1/nodes/import", { text, tags: tags.split(",").map((t) => t.trim()).filter(Boolean) }),
    onSuccess: (r) => {
      toast.success(`已导入 ${r.created.length} 个节点`, r.errors.length ? `${r.errors.length} 条解析失败` : undefined);
      qc.invalidateQueries({ queryKey: ["nodes"] });
      setText(""); setPreview(null); onClose();
    },
    onError: (e) => toast.fromError(e),
  });
  return (
    <Dialog open={open} onClose={onClose} title="批量导入节点" description="支持分享链接（每行一个）、base64 订阅内容、Clash/mihomo YAML 的 proxies。" size="md"
      footer={<><Button variant="outline" onClick={() => parse.mutate()} loading={parse.isPending} disabled={!text.trim()}>预览解析</Button><Button onClick={() => imp.mutate()} loading={imp.isPending} disabled={!text.trim()}>导入</Button></>}>
      <div className="space-y-3">
        <Textarea rows={preview ? 6 : 10} className="mono" placeholder={"vless://uuid@host:443?security=reality&sni=...#HK-1\nss://...\n🇯🇵JP = snell, host, 11831, psk = xxx, version = 5, reuse = true\n或 Clash YAML / base64"} value={text} onChange={(e) => { setText(e.target.value); setPreview(null); }} />
        <Field label="标签" hint="逗号分隔，可用于订阅按标签选节点"><Input value={tags} onChange={(e) => setTags(e.target.value)} placeholder="机场A, 家宽" /></Field>
        {preview && (
          <div>
            <p className="mb-2 text-sm">识别格式：<Code>{preview.format}</Code> · {preview.proxies.length} 个节点</p>
            <div className="max-h-64 overflow-auto rounded-xl border">
              {preview.proxies.map((p, i) => (
                <div key={i} className="flex items-center justify-between border-b px-3 py-1.5 text-sm last:border-0">
                  <span className="break-words">{String(p.name)}</span>
                  <span className="ml-2 shrink-0 text-xs text-muted-foreground">{String(p.type)} · {String(p.server)}:{String(p.port)}</span>
                </div>
              ))}
              {preview.proxies.length === 0 && <p className="p-3 text-sm text-muted-foreground">没有识别到节点</p>}
            </div>
            {preview.errors.length > 0 && <Pre className="mt-2 max-h-32 text-red-500">{preview.errors.join("\n")}</Pre>}
          </div>
        )}
      </div>
    </Dialog>
  );
}

export function NodeEditDialog({ open, onClose, node }: { open: boolean; onClose: () => void; node?: Node }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [mode, setMode] = React.useState<"uri" | "fields">("uri");
  const [f, setF] = React.useState({ uri: "", name: "", protocol: "vless", server: "", port: "", params: "{}", tags: "", enabled: true });
  React.useEffect(() => {
    if (node) {
      setMode("fields");
      setF({ uri: "", name: node.name, protocol: node.protocol, server: node.server, port: String(node.port), params: JSON.stringify(node.params ?? {}, null, 2), tags: node.tags.join(","), enabled: node.enabled });
    } else {
      setMode("uri");
      setF({ uri: "", name: "", protocol: "vless", server: "", port: "", params: "{}", tags: "", enabled: true });
    }
  }, [node, open]);
  const set = (k: string, v: unknown) => setF((p) => ({ ...p, [k]: v }));
  const m = useMutation({
    mutationFn: () => {
      const payload: Record<string, unknown> = { name: f.name, tags: f.tags.split(",").map((t) => t.trim()).filter(Boolean), enabled: f.enabled };
      if (mode === "uri" && !node) payload.uri = f.uri;
      else {
        payload.protocol = f.protocol; payload.server = f.server; payload.port = Number(f.port);
        try { payload.params = JSON.parse(f.params || "{}"); } catch { throw new Error("参数不是合法 JSON"); }
      }
      return node ? put<Node>(`/api/v1/nodes/${node.id}`, payload) : post<Node>("/api/v1/nodes", payload);
    },
    onSuccess: () => { toast.success(node ? "已保存" : "已添加"); qc.invalidateQueries({ queryKey: ["nodes"] }); onClose(); },
    onError: (e) => toast.fromError(e),
  });
  const locked = node?.source === "deployed" || node?.source === "chain";
  return (
    <Dialog open={open} onClose={onClose} title={node ? "编辑节点" : "添加节点"} footer={<><Button variant="outline" onClick={onClose}>取消</Button><Button onClick={() => m.mutate()} loading={m.isPending}>保存</Button></>}>
      {!node && <Tabs className="mb-4" value={mode} onChange={setMode} items={[{ value: "uri", label: "粘贴链接" }, { value: "fields", label: "手动填写" }]} />}
      <div className="grid gap-4">
        {mode === "uri" && !node ? (
          <>
            <Field label="节点链接"><Textarea className="mono" rows={3} value={f.uri} onChange={(e) => set("uri", e.target.value)} placeholder={"vless://... / snell://...\n或 Surge 行：🇯🇵JP = snell, host, 11831, psk = xxx, version = 5"} /></Field>
            <Field label="名称" hint="可选，覆盖链接中的名称"><Input value={f.name} onChange={(e) => set("name", e.target.value)} /></Field>
          </>
        ) : (
          <>
            <Field label="名称"><Input value={f.name} onChange={(e) => set("name", e.target.value)} /></Field>
            {!locked && (
              <>
                <div className="grid grid-cols-3 gap-3">
                  <Field label="协议">
                    <Select value={f.protocol} onChange={(e) => { set("protocol", e.target.value); if (e.target.value === "ssh") { set("port", "22"); set("params", JSON.stringify({ username: "", password: "", "host-key": [] })); } else if (e.target.value === "wireguard") {set("port", "51820");set("params", JSON.stringify({ip: "", "private-key": "", "public-key": "", udp: true, mtu: 1408, "persistent-keepalive": 25}));} else if (e.target.value === "mieru") {set("port", "2999");set("params", JSON.stringify({username: "", password: "", transport: "TCP", udp: true}));} }}>
                      {["vless", "vmess", "trojan", "ss", "hysteria2", "tuic", "anytls", "snell", "socks5", "http", "ssh", "wireguard", "mieru"].map((p) => <option key={p} value={p}>{PROTOCOL_LABELS[p] ?? p}</option>)}
                    </Select>
                  </Field>
                  <Field label="地址"><Input value={f.server} onChange={(e) => set("server", e.target.value)} /></Field>
                  <Field label="端口"><Input type="number" value={f.port} onChange={(e) => set("port", e.target.value)} /></Field>
                </div>
                {f.protocol === "ssh" ? <SSHClientFields value={f.params} onChange={(value) => set("params", value)} /> : f.protocol === "wireguard" || f.protocol === "mieru" ? <TunnelClientFields protocol={f.protocol} value={f.params} onChange={value => set("params", value)} /> : <Field label="参数 (Clash 字段 JSON)" hint="uuid / password / sni / tls / network 等"><Textarea className="mono" rows={6} value={f.params} onChange={(e) => set("params", e.target.value)} /></Field>}
              </>
            )}
          </>
        )}
        <Field label="标签" hint="逗号分隔"><Input value={f.tags} onChange={(e) => set("tags", e.target.value)} /></Field>
        <Switch checked={f.enabled} onChange={(v) => set("enabled", v)} label="启用" />
      </div>
    </Dialog>
  );
}

export function NodeDetailDialog({ node: initialNode, onClose }: { node: Node | null; onClose: () => void }) {
  const qc = useQueryClient();
  const isAdmin = useIsAdmin();
  const detail = useQuery({ queryKey: ["nodes", initialNode?.id, "detail"], queryFn: () => get<Node>(`/api/v1/nodes/${initialNode!.id}`), enabled: isAdmin && !!initialNode && initialNode.source === "deployed" });
  const node = detail.data ?? initialNode;
  const toast = useToast();
  const [edit, setEdit] = React.useState(false);
  const [confirmDel, setConfirmDel] = React.useState(false);
  const [confirmRegen, setConfirmRegen] = React.useState(false);
  const [showQR, setShowQR] = React.useState(false);
 const [trafficDays,setTrafficDays]=React.useState(30);
  const uri = useQuery({ queryKey: ["nodes", node?.id, "uri"], queryFn: () => get<{ uri: string; clash: Record<string, unknown>; surge: string; wireguard?: string; singbox?: Record<string, unknown>; singbox_endpoint?: Record<string, unknown> }>(`/api/v1/nodes/${node!.id}/uri`), enabled: !!node });
  const traffic = useQuery({ queryKey: ["nodes", node?.id, "traffic",trafficDays], queryFn: () => get<Series>(`/api/v1/nodes/${node!.id}/traffic?days=${trafficDays}`), refetchInterval:30000, enabled: !!node && node.source === "deployed" });
  const delM = useMutation({ mutationFn: () => del(`/api/v1/nodes/${node!.id}`), onSuccess: () => { toast.success("已删除"); qc.invalidateQueries({ queryKey: ["nodes"] }); qc.invalidateQueries({ queryKey: ["servers"] }); onClose(); }, onError: (e) => toast.fromError(e) });
  const regen = useMutation({ mutationFn: () => post(`/api/v1/nodes/${node!.id}/regenerate`), onSuccess: () => { toast.success("凭据已重置，订阅将自动更新"); setConfirmRegen(false); qc.invalidateQueries({ queryKey: ["nodes"] }); }, onError: (e) => toast.fromError(e) });
  const toggle = useMutation({ mutationFn: (enabled: boolean) => put(`/api/v1/nodes/${node!.id}`, { enabled }), onSuccess: () => { qc.invalidateQueries({ queryKey: ["nodes"] }); onClose(); }, onError: (e) => toast.fromError(e) });
  if (!node) return null;
  return (
    <>
      <Dialog open={!!node && !edit} onClose={onClose} title={node.name} description={`${PROTOCOL_LABELS[node.protocol] ?? node.protocol} · ${node.server}:${node.port}`} wide
        footer={
          <>
            {!node.share_id && <Button variant="ghost" className="text-red-500" onClick={() => setConfirmDel(true)}><Trash2 className="h-4 w-4" /> 删除</Button>}
            {node.source === "deployed" && <Button variant="outline" onClick={() => setConfirmRegen(true)}><RotateCcw className="h-4 w-4" /> 重置凭据</Button>}
            <Button variant="outline" onClick={() => toggle.mutate(!node.enabled)} loading={toggle.isPending}>{node.enabled ? "禁用" : "启用"}</Button>
            <Button onClick={() => setEdit(true)}><Pencil className="h-4 w-4" /> 编辑</Button>
          </>
        }>
        <div className="grid gap-4 lg:grid-cols-2">
          <div className="space-y-3">
            <div className="flex flex-wrap gap-2"><SourceBadge n={node} /><ProtocolBadge p={node.protocol} />{node.core && node.source === "deployed" && <Badge variant="outline">内核 {node.core}</Badge>}{node.chain_front_name && <Badge variant="info">经 {node.chain_front_name}</Badge>}{node.tags.map((t) => <Badge key={t} variant="secondary">{t}</Badge>)}</div>
            <p className="text-xs text-muted-foreground">创建于 {fmtDate(node.created_at)}</p>
            {uri.data && (
              <>
                {!uri.data.uri && <p className="text-xs text-muted-foreground">此协议使用客户端配置，不提供通用分享链接。</p>}
                {uri.data.uri && <div>
                  <div className="mb-1 flex items-center justify-between"><span className="text-xs font-medium text-muted-foreground">分享链接</span>
                    <div className="flex gap-1">
                      <Button size="sm" variant="ghost" onClick={() => setShowQR((v) => !v)}><QrCode className="h-4 w-4" /></Button>
                      <Button size="sm" variant="ghost" onClick={async () => { await copyText(uri.data!.uri); toast.success("已复制"); }}><Copy className="h-4 w-4" /></Button>
                    </div>
                  </div>
                  <Pre className="whitespace-pre-wrap break-all">{uri.data.uri}</Pre>
                  {showQR && <QR text={uri.data.uri} />}
                </div>}
                {uri.data.surge && <div>
                  <div className="mb-1 flex items-center justify-between"><span className="text-xs font-medium text-muted-foreground">Surge</span><Button size="sm" variant="ghost" onClick={async () => { await copyText(uri.data!.surge); toast.success("已复制"); }}><Copy className="h-4 w-4" /></Button></div>
                  <Pre className="whitespace-pre-wrap break-all">{uri.data.surge}</Pre>
                </div>}
              </>
            )}
          </div>
          <div className="space-y-3">
            {uri.data && (
              <div>
                <div className="mb-1 flex items-center justify-between"><span className="text-xs font-medium text-muted-foreground">{["ssh", "wireguard", "mieru"].includes(node.protocol) ? "mihomo" : "Clash / mihomo"}</span><Button size="sm" variant="ghost" onClick={async () => { await copyText(JSON.stringify(uri.data!.clash, null, 2)); toast.success("已复制"); }}><Copy className="h-4 w-4" /></Button></div>
                <Pre className="max-h-64">{JSON.stringify(uri.data.clash, null, 2)}</Pre>
                {uri.data.wireguard && <div className="mt-3"><div className="mb-1 flex items-center justify-between"><span className="text-xs text-muted-foreground">标准 WireGuard 配置</span><Button size="sm" variant="ghost" onClick={async () => {await copyText(uri.data!.wireguard!);toast.success("已复制");}}><Copy className="h-4 w-4" /></Button></div><Pre className="max-h-64">{uri.data.wireguard}</Pre></div>}
                {(uri.data.singbox || uri.data.singbox_endpoint) && <div className="mt-3"><span className="text-xs text-muted-foreground">{uri.data.singbox_endpoint ? "sing-box 1.11+ endpoint" : "sing-box outbound"}</span><Pre className="max-h-64">{JSON.stringify(uri.data.singbox_endpoint ?? uri.data.singbox,null,2)}</Pre></div>}
              </div>
            )}
            {node.source === "deployed" && <div className="space-y-2">
              <Select aria-label="节点流量时间范围" value={trafficDays} onChange={e=>setTrafficDays(Number(e.target.value))}>{[7,30,90].map(d=><option key={d} value={d}>近 {d} 天流量</option>)}</Select>
              {traffic.isError ? <p className="text-sm text-destructive">流量加载失败，请稍后重试</p> : traffic.isLoading ? <Spinner /> : traffic.data?.has_data ? <><TrafficIO inbound={traffic.data.total_up} outbound={traffic.data.total_down}/><TrafficBars points={traffic.data.points} height={160}/><p className="text-xs text-muted-foreground">仅汇总已采集数据；无记录的日期不代表实际未使用。</p></> : <p className="text-sm text-muted-foreground">该时间范围暂无用量记录</p>}
            </div>}
          </div>
        </div>
        {isAdmin && node.source === "deployed" && node.server_id && <NodeNetwork key={node.id} nodeID={node.id} serverID={node.server_id} onNavigate={onClose} />}
      </Dialog>
      <NodeEditDialog open={edit} onClose={() => { setEdit(false); onClose(); }} node={node} />
      <Confirm open={confirmDel} onClose={() => setConfirmDel(false)} onConfirm={() => delM.mutate()} loading={delM.isPending} destructive title="删除节点？" description={node.source === "deployed" ? "该节点会从服务器上撤下，使用它的订阅将不再包含它。" : "使用它的订阅将不再包含它。"} />
      <Confirm open={confirmRegen} onClose={() => setConfirmRegen(false)} onConfirm={() => regen.mutate()} loading={regen.isPending} title="重置凭据？" description="将重新生成 UUID/密码/Reality 密钥，服务器应用配置后旧凭据失效；订阅链接不变，请客户端更新订阅。" />
    </>
  );
}

export function QR({ text, className }: { text: string; className?: string }) {
  const [svg, setSvg] = React.useState<string>("");
  React.useEffect(() => {
    let alive = true;
    import("@/lib/qr").then((m) => m.toSVG(text)).then((s) => alive && setSvg(s)).catch(() => alive && setSvg(""));
    return () => { alive = false; };
  }, [text]);
  if (!svg) return null;
  return <div className={cn("mx-auto mt-2 w-48 rounded-md bg-white p-2", className)}><img className="h-full w-full" alt="节点二维码" src={`data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`} /></div>;
}
