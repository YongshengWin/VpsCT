import * as React from "react";
import { DndContext, DragOverlay, PointerSensor, closestCenter, useDraggable, useDroppable, useSensor, useSensors, type DragEndEvent, type DragStartEvent } from "@dnd-kit/core";
import { SortableContext, arrayMove, useSortable, verticalListSortingStrategy, horizontalListSortingStrategy } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { GripVertical, Plus, Trash2, X, Search, Wand2 } from "lucide-react";
import { normalizeGroups, type ProxyGroup, type GroupType } from "@/lib/types";
import { cn } from "@/lib/utils";
import { Badge, Button, Field, Input, Select, Switch, Code } from "@/components/ui";

export const GROUP_TYPES: { value: GroupType; label: string; hint: string }[] = [
  { value: "select", label: "手动选择", hint: "客户端里手动切换" },
  { value: "url-test", label: "自动测速", hint: "延迟最低者优先" },
  { value: "fallback", label: "故障转移", hint: "按顺序取第一个可用" },
  { value: "load-balance", label: "负载均衡", hint: "多节点分摊" },
  { value: "relay", label: "链式中转", hint: "按顺序依次经过（mihomo 已弃用，建议用节点前置）" },
];

export const POLICIES = ["DIRECT", "REJECT"];

export interface Candidate {
  name: string;
  kind: "node" | "chain" | "group" | "policy";
  sub?: string; // protocol / description
}

interface Props {
  groups: ProxyGroup[];
  onChange: (g: ProxyGroup[]) => void;
  nodes: Candidate[]; // nodes + chains available to the subscription
  className?: string;
  /** 预设弹窗里没有真实节点，用两栏 + 更直白的收节点说明。 */
  variant?: "page" | "preset";
}

// Drag ids: palette item "p:<name>", group member "m:<groupIndex>:<name>", group card "g:<index>".

function parseMember(id: string): { gi: number; name: string } | null {
  if (!id.startsWith("m:")) return null;
  const rest = id.slice(2);
  const idx = rest.indexOf(":");
  if (idx < 0) return null;
  return { gi: Number(rest.slice(0, idx)), name: rest.slice(idx + 1) };
}

export function GroupEditor({ groups: rawGroups, onChange, nodes, className, variant = "page" }: Props) {
  const preset = variant === "preset";
  // Presets / API payloads may carry `proxies: null`; every path below assumes an array.
  const groups = React.useMemo(() => normalizeGroups(rawGroups), [rawGroups]);
  const [active, setActive] = React.useState<number>(groups.length ? 0 : -1);
  const [dragging, setDragging] = React.useState<string | null>(null);
  const [search, setSearch] = React.useState("");
  const sensors = useSensors(useSensor(PointerSensor, { activationConstraint: { distance: 5 } }));

  React.useEffect(() => {
    if (active >= groups.length) setActive(groups.length - 1);
    else if (active < 0 && groups.length > 0) setActive(0); // e.g. a preset was just applied
  }, [groups.length, active]);

  const update = (i: number, patch: Partial<ProxyGroup>) => onChange(groups.map((g, j) => (j === i ? { ...g, ...patch } : g)));
  const addGroup = (type: GroupType = "select") => {
    const base = type === "select" ? "节点选择" : type === "url-test" ? "自动选择" : type === "fallback" ? "故障转移" : type === "load-balance" ? "负载均衡" : "中转";
    let name = base;
    let i = 2;
    while (groups.some((g) => g.name === name)) name = `${base} ${i++}`;
    const g: ProxyGroup = { name, type, proxies: [], include_all: type !== "select" };
    onChange([...groups, g]);
    setActive(groups.length);
  };
  const removeGroup = (i: number) => {
    const name = groups[i].name;
    onChange(groups.filter((_, j) => j !== i).map((g) => ({ ...g, proxies: g.proxies.filter((p) => p !== name) })));
  };
  const rename = (i: number, name: string) => {
    const old = groups[i].name;
    onChange(groups.map((g, j) => (j === i ? { ...g, name } : { ...g, proxies: g.proxies.map((p) => (p === old ? name : p)) })));
  };
  const addMember = (i: number, name: string) => {
    const g = groups[i];
    if (!g || g.proxies.includes(name) || name === g.name) return;
    update(i, { proxies: [...g.proxies, name] });
  };
  const removeMember = (i: number, name: string) => update(i, { proxies: groups[i].proxies.filter((p) => p !== name) });

  const candidates: Candidate[] = React.useMemo(() => {
    const gs: Candidate[] = groups.map((g) => ({ name: g.name, kind: "group", sub: GROUP_TYPES.find((t) => t.value === g.type)?.label }));
    const ps: Candidate[] = POLICIES.map((p) => ({ name: p, kind: "policy" }));
    return [...gs, ...nodes, ...ps];
  }, [groups, nodes]);
  const filtered = candidates.filter((c) => !search || c.name.toLowerCase().includes(search.toLowerCase()));

  const onDragStart = (e: DragStartEvent) => setDragging(String(e.active.id));
  const onDragEnd = (e: DragEndEvent) => {
    setDragging(null);
    const { active: a, over } = e;
    if (!over) return;
    const aid = String(a.id);
    const oid = String(over.id);
    // reorder groups
    if (aid.startsWith("g:") && oid.startsWith("g:")) {
      const from = Number(aid.slice(2));
      const to = Number(oid.slice(2));
      if (from !== to) {
        onChange(arrayMove(groups, from, to));
        setActive(to);
      }
      return;
    }
    // drop palette item into a group drop zone or onto a member
    const targetGroup = (): number | null => {
      if (oid.startsWith("drop:")) return Number(oid.slice(5));
      if (oid.startsWith("m:")) return Number(oid.split(":")[1]);
      return null;
    };
    if (aid.startsWith("p:")) {
      const gi = targetGroup();
      if (gi !== null) addMember(gi, aid.slice(2));
      return;
    }
    // reorder members inside a group
    const am = parseMember(aid);
    const om = parseMember(oid);
    if (am && om && am.gi === om.gi) {
      const grp = groups[am.gi];
      const from = grp.proxies.indexOf(am.name);
      const to = grp.proxies.indexOf(om.name);
      if (from >= 0 && to >= 0 && from !== to) update(am.gi, { proxies: arrayMove(grp.proxies, from, to) });
    }
  };

  const g = active >= 0 ? groups[active] : undefined;
  const extraMembers = filtered.filter((c) => c.kind === "group" || c.kind === "policy");

  return (
    <DndContext sensors={sensors} collisionDetection={closestCenter} onDragStart={onDragStart} onDragEnd={onDragEnd}>
      <div className={cn(
        "grid min-h-0 min-w-0 items-stretch gap-4",
        preset ? "h-full md:grid-cols-[15rem_minmax(0,1fr)]" : "h-[min(40rem,calc(100vh-14rem))] xl:grid-cols-[16rem_minmax(0,1fr)_18rem]",
        className,
      )}>
        <div className="flex min-h-0 min-w-0 flex-col overflow-hidden rounded-lg border p-3">
          <div className="shrink-0 space-y-2">
            <p className="text-sm font-medium">代理组 ({groups.length})</p>
            <Select className="h-9 text-xs" value="" onChange={(e) => e.target.value && addGroup(e.target.value as GroupType)} aria-label="添加策略组，选的是怎么挑节点，不是协议">
              <option value="">+ 添加组（先选类型）</option>
              {GROUP_TYPES.map((t) => <option key={t.value} value={t.value}>{t.label}</option>)}
            </Select>
          </div>
          <SortableContext items={groups.map((_, i) => `g:${i}`)} strategy={verticalListSortingStrategy}>
            <ul className="mt-2 min-h-0 flex-1 space-y-1 overflow-y-auto">
              {groups.map((gr, i) => <GroupItem key={`g:${i}`} id={`g:${i}`} g={gr} active={i === active} onClick={() => setActive(i)} onRemove={() => removeGroup(i)} />)}
            </ul>
          </SortableContext>
          {groups.length === 0 && <p className="mt-2 rounded-md border border-dashed p-3 text-xs text-muted-foreground">{preset ? "还没有组。上面选一种类型加一组，例如「手动选择」或「自动测速」。" : "空着会沿用模板里的分组。想只要「手动 + 自动」用右上角「套用分组与规则」。上面下拉是选组的挑节点方式（手动/测速），不是 VLESS / Snell。"}</p>}
        </div>

        <div className="flex min-h-0 min-w-0 flex-col overflow-hidden rounded-lg border">
          {!g ? (
            <p className="p-4 text-sm text-muted-foreground">{preset ? "左边点一个组，右边改它怎么收节点。" : "没有自定义组。客户端会用模板里的策略组；不必在这里选协议。"}</p>
          ) : (
            <div className="min-h-0 flex-1 space-y-4 overflow-y-auto p-4">
              <div className="grid min-w-0 gap-3 sm:grid-cols-2">
                <Field label="组名" className="min-w-0"><Input value={g.name} onChange={(e) => rename(active, e.target.value)} /></Field>
                <Field label="怎么挑节点" hint={GROUP_TYPES.find((t) => t.value === g.type)?.hint} className="min-w-0">
                  <Select value={g.type} onChange={(e) => update(active, { type: e.target.value as GroupType })}>
                    {GROUP_TYPES.map((t) => <option key={t.value} value={t.value}>{t.label}</option>)}
                  </Select>
                </Field>
              </div>

              <div className="space-y-3 rounded-xl bg-muted/50 p-3">
                <p className="text-sm font-medium">这个组的节点从哪来</p>
                <Switch checked={!!g.include_all} onChange={(v) => update(active, { include_all: v })} label="自动收下全部节点" />
                <p className="text-xs leading-5 text-muted-foreground">
                  {g.include_all
                    ? (preset ? "每次生成订阅时，从该订阅已选的节点中匹配成员；新增的匹配节点会随订阅更新加入。可在下面按名称筛选。" : "当前订阅勾选的节点都会进这个组。正则用来只留一部分。")
                    : "未开启全部节点时，可用下方筛选条件自动匹配，或只使用手动添加的组名、DIRECT 等成员。"}
                </p>
                <div className="grid min-w-0 gap-3 sm:grid-cols-2">
                  <Field label="只留下（正则）" hint="按节点名称匹配；香港|HK 表示包含任一项，(?i) 可忽略大小写" className="min-w-0">
                    <Input className="mono" value={g.filter ?? ""} onChange={(e) => update(active, { filter: e.target.value })} placeholder="留空 = 全收" />
                  </Field>
                  <Field label="再排除（正则）" hint="例：过期|剩余" className="min-w-0">
                    <Input className="mono" value={g.exclude_filter ?? ""} onChange={(e) => update(active, { exclude_filter: e.target.value })} placeholder="留空 = 不排除" />
                  </Field>
                </div>
              </div>

              <div>
                <div className="mb-1.5 flex items-start justify-between gap-2">
                  <div className="min-w-0">
                    <p className="text-sm font-medium">额外成员</p>
                    <p className="text-xs leading-5 text-muted-foreground">{preset ? "这里加的是其它组名或 DIRECT，不是服务器节点。比如手动组里放「自动测速」和 DIRECT。" : "从右侧点一下或拖进来。拖成员可排序。"}</p>
                  </div>
                  {g.proxies.length > 0 && <Button size="sm" variant="ghost" className="shrink-0" onClick={() => update(active, { proxies: [] })}>清空</Button>}
                </div>
                <DropZone id={`drop:${active}`} highlight={!!dragging?.startsWith("p:")}>
                  <SortableContext items={g.proxies.map((p) => `m:${active}:${p}`)} strategy={horizontalListSortingStrategy}>
                    <div className="flex min-h-[3rem] flex-wrap gap-1.5">
                      {g.proxies.map((p) => <MemberChip key={p} id={`m:${active}:${p}`} name={p} kind={candidates.find((c) => c.name === p)?.kind} onRemove={() => removeMember(active, p)} />)}
                      {g.proxies.length === 0 && <span className="self-center text-xs text-muted-foreground">{g.include_all ? "没有额外成员也可以，节点靠上面自动收。" : "空组生成时会填 DIRECT。"}</span>}
                    </div>
                  </SortableContext>
                </DropZone>
                {preset && (
                  <div className="mt-2 flex flex-wrap gap-1.5">
                    {extraMembers.map((c) => {
                      const disabled = g.proxies.includes(c.name) || c.name === g.name;
                      return (
                        <button
                          key={`${c.kind}:${c.name}`}
                          type="button"
                          disabled={disabled}
                          onClick={() => addMember(active, c.name)}
                          className={cn("rounded-lg border px-2 py-1 text-xs", disabled ? "opacity-40" : "hover:bg-accent")}
                        >
                          {c.name}
                        </button>
                      );
                    })}
                  </div>
                )}
              </div>

              <details className="rounded-xl border border-border/70 px-3 py-2">
                <summary className="cursor-pointer text-sm font-medium text-muted-foreground">高级：隐藏、图标、前置代理、测速</summary>
                <div className="mt-3 grid min-w-0 gap-3 sm:grid-cols-2">
                  <div className="sm:col-span-2 flex flex-wrap gap-4">
                    {(g.type === "url-test" || g.type === "fallback" || g.type === "load-balance") && <Switch checked={!!g.lazy} onChange={(v) => update(active, { lazy: v })} label="未选中时不测速" />}
                    <Switch checked={!!g.hidden} onChange={(v) => update(active, { hidden: v })} label="在客户端隐藏" />
                  </div>
                  {(g.type === "url-test" || g.type === "fallback" || g.type === "load-balance") && (
                    <>
                      <Field label="测速 URL" className="min-w-0 sm:col-span-2"><Input value={g.url ?? ""} onChange={(e) => update(active, { url: e.target.value })} placeholder="https://www.gstatic.com/generate_204" /></Field>
                      <Field label="间隔（秒）" className="min-w-0"><Input type="number" value={g.interval ?? ""} onChange={(e) => update(active, { interval: Number(e.target.value) || 0 })} placeholder="300" /></Field>
                      {g.type === "url-test" && <Field label="容差（ms）" className="min-w-0"><Input type="number" value={g.tolerance ?? ""} onChange={(e) => update(active, { tolerance: Number(e.target.value) || 0 })} placeholder="50" /></Field>}
                      {g.type === "load-balance" && (
                        <Field label="策略" className="min-w-0">
                          <Select value={g.strategy ?? ""} onChange={(e) => update(active, { strategy: e.target.value })}>
                            <option value="">consistent-hashing</option>
                            <option value="round-robin">round-robin</option>
                            <option value="sticky-sessions">sticky-sessions</option>
                          </Select>
                        </Field>
                      )}
                    </>
                  )}
                  <Field label="前置代理" hint="本组所有节点先走它再出去" className="min-w-0">
                    <Select value={g.dialer_proxy ?? ""} onChange={(e) => update(active, { dialer_proxy: e.target.value })}>
                      <option value="">不使用</option>
                      {candidates.filter((c) => c.kind !== "policy" && c.name !== g.name).map((c) => <option key={c.name} value={c.name}>{c.name}</option>)}
                    </Select>
                  </Field>
                  <Field label="图标 URL" hint="部分客户端能显示" className="min-w-0"><Input value={g.icon ?? ""} onChange={(e) => update(active, { icon: e.target.value })} /></Field>
                </div>
              </details>

            </div>
          )}
        </div>

        {!preset && (
          <div className="flex min-h-0 min-w-0 flex-col overflow-hidden rounded-lg border p-3">
            <p className="text-sm font-medium">候选（点一下或拖进组）</p>
            <div className="relative">
              <Search className="absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
              <Input className="pl-8" placeholder="搜索节点 / 组" value={search} onChange={(e) => setSearch(e.target.value)} />
            </div>
            <div className="min-h-0 flex-1 space-y-1 overflow-y-auto rounded-md border p-2">
              {filtered.length === 0 && <p className="p-2 text-xs text-muted-foreground">无匹配。节点候选来自「节点选择」页勾选的节点。</p>}
              {filtered.map((c) => (
                <PaletteItem key={`${c.kind}:${c.name}`} c={c} disabled={!g || g.proxies.includes(c.name) || c.name === g.name} onAdd={() => g && addMember(active, c.name)} />
              ))}
            </div>
            {g && filtered.some((c) => c.kind === "node" && !g.proxies.includes(c.name)) && (
              <Button size="sm" variant="outline" className="w-full" onClick={() => filtered.filter((c) => c.kind === "node").forEach((c) => addMember(active, c.name))}><Wand2 className="h-4 w-4" /> 添加全部匹配节点</Button>
            )}
          </div>
        )}
      </div>
      <DragOverlay dropAnimation={null}>{dragging ? <Badge className="shadow-lg">{dragging.replace(/^p:|^m:\d+:/, "").replace(/^g:(\d+)$/, (_, i) => groups[Number(i)]?.name ?? "")}</Badge> : null}</DragOverlay>
    </DndContext>
  );
}

function GroupItem({ id, g, active, onClick, onRemove }: { id: string; g: ProxyGroup; active: boolean; onClick: () => void; onRemove: () => void }) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({ id });
  const style = { transform: CSS.Transform.toString(transform), transition, opacity: isDragging ? 0.5 : 1 };
  const count = g.proxies.length + (g.include_all || g.filter ? 1 : 0);
  return (
    <li ref={setNodeRef} style={style} className={cn("group flex items-center gap-1 rounded-md border px-2 py-1.5 text-sm", active ? "border-primary bg-primary/5" : "hover:bg-accent/40")}>
      <button {...attributes} {...listeners} className="cursor-grab touch-none text-muted-foreground"><GripVertical className="h-4 w-4" /></button>
      <button className="min-w-0 flex-1 text-left" onClick={onClick}>
        <p className="truncate font-medium">{g.name}</p>
        <p className="truncate text-[11px] text-muted-foreground">{GROUP_TYPES.find((t) => t.value === g.type)?.label} · {g.include_all ? (g.filter ? `只留 ${g.filter}` : "全部节点") : g.filter ? `正则 ${g.filter}` : `${count} 项`}{g.dialer_proxy ? ` · 经 ${g.dialer_proxy}` : ""}</p>
      </button>
      <button type="button" title="删除此组" onClick={(e) => { e.stopPropagation(); onRemove(); }} className={cn("shrink-0 rounded p-1 text-muted-foreground hover:bg-accent hover:text-red-500", active ? "opacity-100" : "opacity-0 group-hover:opacity-100")}>
        <Trash2 className="h-3.5 w-3.5" />
      </button>
    </li>
  );
}

function PaletteItem({ c, disabled, onAdd }: { c: Candidate; disabled: boolean; onAdd: () => void }) {
  const { attributes, listeners, setNodeRef, isDragging } = useDraggable({ id: `p:${c.name}`, disabled });
  return (
    <div ref={setNodeRef} {...attributes} {...listeners} className={cn("flex items-center gap-2 rounded-md px-2 py-1.5 text-sm", disabled ? "opacity-40" : "cursor-grab hover:bg-accent/50", isDragging && "opacity-30")}>
      <KindDot kind={c.kind} />
      <span className="min-w-0 flex-1 break-words">{c.name}</span>
      {c.sub && <span className="shrink-0 text-[10px] text-muted-foreground">{c.sub}</span>}
      <button disabled={disabled} onClick={onAdd} className="rounded p-0.5 text-muted-foreground hover:bg-accent hover:text-foreground disabled:opacity-0" title="添加"><Plus className="h-3.5 w-3.5" /></button>
    </div>
  );
}

function MemberChip({ id, name, kind, onRemove }: { id: string; name: string; kind?: Candidate["kind"]; onRemove: () => void }) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({ id });
  const style = { transform: CSS.Transform.toString(transform), transition, opacity: isDragging ? 0.4 : 1 };
  return (
    <span ref={setNodeRef} style={style} className="inline-flex max-w-full items-center gap-1 rounded-md border bg-background px-2 py-1 text-xs">
      <button {...attributes} {...listeners} className="cursor-grab touch-none text-muted-foreground"><GripVertical className="h-3 w-3" /></button>
      <KindDot kind={kind} />
      <span className="break-words">{name}</span>
      <button onClick={onRemove} className="text-muted-foreground hover:text-foreground"><X className="h-3 w-3" /></button>
    </span>
  );
}

function DropZone({ id, highlight, children }: { id: string; highlight: boolean; children: React.ReactNode }) {
  const { setNodeRef, isOver } = useDroppable({ id });
  return (
    <div ref={setNodeRef} className={cn("rounded-md border border-dashed p-2 transition-colors", highlight && "border-primary/60 bg-primary/5", isOver && "bg-primary/10")}>
      {children}
    </div>
  );
}

function KindDot({ kind }: { kind?: Candidate["kind"] }) {
  const color = kind === "group" ? "bg-violet-500" : kind === "chain" ? "bg-amber-500" : kind === "policy" ? "bg-slate-400" : "bg-emerald-500";
  return <span className={cn("h-2 w-2 shrink-0 rounded-full", color)} title={kind} />;
}

export function RulesHint() {
  return (
    <p className="text-xs text-muted-foreground">
      每行一条 mihomo 规则，例如 <Code>DOMAIN-SUFFIX,google.com,节点选择</Code>、<Code>GEOIP,CN,DIRECT</Code>、<Code>MATCH,节点选择</Code>。策略名须为已定义的代理组或 DIRECT/REJECT。
    </p>
  );
}
