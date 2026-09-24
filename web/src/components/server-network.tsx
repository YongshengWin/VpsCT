import * as React from "react";
import { useQuery } from "@tanstack/react-query";
import { get } from "@/lib/api";
import type { NetworkView, Series, Server } from "@/lib/types";
import { fmtAgo, fmtBytes, fmtRate } from "@/lib/utils";
import { Badge, Card, CardContent, CardHeader, CardTitle, Empty, Field, Select, Spinner, Switch, Table, Td, Th, Tr } from "@/components/ui";
import { TrafficBars } from "@/components/charts";
import { TrafficIO } from "@/components/traffic-ways";
import { InterfaceHistory } from "@/components/interface-history";
import { NetworkBilling } from "@/components/network-billing";

const kinds: Record<string, string> = { physical: "物理 / 虚拟机设备", loopback: "回环", wireguard: "WireGuard", bridge: "网桥", veth: "虚拟网线", tun: "TUN/TAP", dummy: "虚拟接口", unknown: "未识别" };

export function ServerNetwork({ server }: { server: Server }) {
  const q = useQuery({ queryKey: ["server-network", server.id], queryFn: () => get<NetworkView>(`/api/v1/servers/${server.id}/network`), refetchInterval: 10000 });
  const [selected, setSelected] = React.useState("");
  const [days, setDays] = React.useState("7");
  const [showAll, setShowAll] = React.useState(false);
  const [showHistory, setShowHistory] = React.useState(false);
  const allItems = [...(q.data?.interfaces ?? [])].sort((a, b) => Number(b.present) - Number(a.present) || a.interface.index - b.interface.index);
  const items = showAll ? allItems : allItems.filter((n) => !["loopback", "veth", "bridge"].includes(n.interface.kind));
  const selectedID = items.some((n) => String(n.id) === selected) ? selected : String(items[0]?.id ?? "");
  const history = useQuery({ queryKey: ["interface-traffic", server.id, selectedID, days], enabled: !!selectedID && showHistory, queryFn: () => get<Series>(`/api/v1/servers/${server.id}/interfaces/${selectedID}/traffic?days=${days}`), refetchInterval: 30000 });
  if (q.isPending) return <div className="mt-4"><Spinner /></div>;
  if (q.isError) return <Empty title="网卡信息加载失败" description={q.error.message} />;
  const snapshot = q.data.snapshot;
  if (!snapshot) return <div className="mt-4 space-y-4"><NetworkBilling server={server} inventory={q.data} /><Empty title="等待多网卡数据" description="支持多网卡采集的 agent 会在接入后上报。旧版 agent 仍可正常使用原有流量统计。" /></div>;
  if (snapshot.status === "unsupported") return <div className="mt-4 space-y-4"><NetworkBilling server={server} inventory={q.data} /><Empty title="当前系统不支持网卡采集" description="多网卡采集需要 Linux agent。" /></div>;
  const fresh = server.agent_status === "online" && !!q.data.received_at && Date.now() - Date.parse(q.data.received_at) < 10 * 60 * 1000;
  const observed = new Set(snapshot.interfaces.map((n) => n.id));
  const complete = snapshot.status === "ok";
  return <div className="mt-4 space-y-4">
    <Card>
      <CardHeader><CardTitle>这台 VPS 的网卡</CardTitle></CardHeader>
      <CardContent className="space-y-4">
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <Badge variant={!fresh || !complete ? "warning" : "success"}>{!fresh ? "数据未更新" : complete ? "正在采集" : snapshot.status === "error" ? "采集失败" : "部分数据不可用"}</Badge>
          <span className="text-muted-foreground">最近更新：{fmtAgo(q.data.received_at ?? undefined)}</span>
          {snapshot.error && <span role="status">{snapshot.error}</span>}
        </div>
        {!items.length ? <Empty title="尚无可展示的网卡" description="等待下一次采集，或在下方详细参数中显示全部网卡。" /> : <div className="grid gap-3 md:grid-cols-2">
          {items.map((item) => {
            const n = item.interface;
            const current = fresh && observed.has(n.id) && snapshot.status !== "error" && item.present;
            return <div key={item.id} className="rounded-lg border p-4">
              <div className="flex items-center justify-between gap-2"><h3 className="font-medium">{n.name}</h3><Badge variant={!current ? "secondary" : n.up && n.carrier ? "success" : "warning"}>{!item.present ? "已消失" : !current ? "状态未更新" : n.up && n.carrier ? "可用" : "未连接"}</Badge></div>
              <p className="mt-2 text-sm">接收 {current && n.rate_valid ? fmtRate(n.rx_rate) : "—"} <span className="mx-1 text-muted-foreground">·</span> 发出 {current && n.rate_valid ? fmtRate(n.tx_rate) : "—"}</p>
              <p className="mt-1 truncate text-xs text-muted-foreground" title={n.addresses?.join("、")}>{n.addresses?.[0] ?? "没有 IP 地址"}{n.addresses && n.addresses.length > 1 ? ` 等 ${n.addresses.length} 个地址` : ""}</p>
            </div>;
          })}
        </div>}
        <p className="text-xs text-muted-foreground">接收和发出是网卡当前的传输速率；配额按下方选定的来源统计。</p>
      </CardContent>
    </Card>
    <NetworkBilling server={server} inventory={q.data} />
    <details className="rounded-xl border bg-card p-4" onToggle={(event) => setShowHistory(event.currentTarget.open)}>
      <summary className="cursor-pointer font-medium">查看流量历史与旧网卡</summary>
      {showHistory && <div className="mt-4 space-y-4">
        <Card>
          <CardHeader><CardTitle>单网卡流量历史</CardTitle></CardHeader>
          <CardContent>
            {!items.length ? <p className="text-sm text-muted-foreground">暂无可查询的网卡。</p> : <>
              <div className="mb-4 flex flex-wrap gap-4">
                <Field label="网卡"><Select aria-label="历史网卡" value={selectedID} onChange={(e) => setSelected(e.target.value)}>{items.map((item) => <option key={item.id} value={item.id}>{item.interface.name} · #{item.id}{item.present ? "" : "（已消失）"}</option>)}</Select></Field>
                <Field label="时间范围"><Select aria-label="网卡历史时间范围" value={days} onChange={(e) => setDays(e.target.value)}><option value="7">近 7 天</option><option value="30">近 30 天</option><option value="90">近 90 天</option></Select></Field>
              </div>
              {history.isPending ? <Spinner /> : history.isError ? <p className="text-sm text-destructive">历史加载失败：{history.error.message}</p> : history.data?.has_data ? <><TrafficIO inbound={history.data.total_up} outbound={history.data.total_down} /><TrafficBars points={history.data.points} /></> : <p className="text-sm text-muted-foreground">所选时间范围暂无用量记录。</p>}
            </>}
          </CardContent>
        </Card>
        <InterfaceHistory serverID={server.id} />
      </div>}
    </details>
    <details className="rounded-xl border bg-card p-4">
      <summary className="cursor-pointer font-medium">查看 IP、路由等详细参数</summary>
      <div className="mt-4 space-y-4">
        <p className="text-xs text-muted-foreground">隧道、网桥和物理网卡可能记录同一批流量，不能直接相加。</p>
        <Switch checked={showAll} onChange={setShowAll} label="显示回环、网桥和 veth 接口" />
        {!items.length ? <Empty title="暂无网卡" description={allItems.length ? "打开上方开关查看其他接口。" : "等待下一次采集。"} /> : <Table className="min-w-[840px]">
        <thead><tr className="border-b"><Th>网卡</Th><Th>状态 / 路由</Th><Th>本机地址</Th><Th>接收 / 发送速率</Th><Th>设备累计接收 / 发送</Th></tr></thead>
        <tbody>{items.map((item) => {
          const n = item.interface;
          const current = fresh && observed.has(n.id) && snapshot.status !== "error" && item.present;
          return <Tr key={item.id}>
            <Td><div className="font-medium">{n.name}</div><div className="text-xs text-muted-foreground">{kinds[n.kind] ?? n.kind} · MTU {n.mtu}</div><div className="text-xs text-muted-foreground">{n.mac || "无 MAC"}</div></Td>
            <Td><div className="flex flex-wrap gap-1"><Badge variant={!current ? "secondary" : n.up && n.carrier ? "success" : "warning"}>{!item.present ? "已消失" : !current ? "历史状态" : !n.up ? "已关闭" : n.carrier ? "链路正常" : "无载波"}</Badge>{n.default_ipv4 && <Badge variant="outline">IPv4 默认</Badge>}{n.default_ipv6 && <Badge variant="outline">IPv6 默认</Badge>}</div>{n.master_index ? <div className="mt-1 text-xs text-muted-foreground">上级接口 #{n.master_index}</div> : null}</Td>
            <Td><div className="max-w-xs font-mono text-xs">{n.addresses?.length ? n.addresses.map((ip) => <div className="whitespace-nowrap" key={ip}>{ip}</div>) : "无地址"}</div></Td>
            <Td><div>{current && n.rate_valid ? fmtRate(n.rx_rate) : "—"}</div><div>{current && n.rate_valid ? fmtRate(n.tx_rate) : "—"}</div></Td>
            <Td><div>{n.counters_valid ? fmtBytes(n.rx) : "—"}</div><div>{n.counters_valid ? fmtBytes(n.tx) : "—"}</div>{!current && <div className="text-xs text-muted-foreground">最后出现 {fmtAgo(item.last_seen_at)}</div>}</Td>
          </Tr>;
        })}</tbody>
        </Table>}
      </div>
    </details>
  </div>;
}
