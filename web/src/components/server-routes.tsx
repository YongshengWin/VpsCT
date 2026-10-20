import { useQuery } from "@tanstack/react-query";
import { useSearchParams } from "react-router-dom";
import { Route, ArrowRightLeft, Network, History } from "lucide-react";
import { AccessRoutes } from "@/components/access-routes";
import { ManagedTransits } from "@/components/managed-transits";
import { ServerEgress } from "@/components/server-egress";
import { ServerForwards } from "@/components/server-forwards";
import { NetworkOperations } from "@/components/network-operations";
import { Field, Select } from "@/components/ui";
import { get } from "@/lib/api";
import type { NetworkView, Server } from "@/lib/types";

const views = [
  { value: "forward", title: "转发线路", description: "入口 → 落地，可选中转", icon: Route },
  { value: "exit", title: "节点出口", description: "为已有节点更换出网方式", icon: ArrowRightLeft },
  { value: "ports", title: "单段转发", description: "自定义一个端口的转发", icon: Network },
  { value: "history", title: "变更记录", description: "查看配置应用结果", icon: History },
] as const;

export function ServerRoutes({ server, initialView = "routes" }: { server: Server; initialView?: "routes" | "egress" | "forwards" }) {
  const [params, setParams] = useSearchParams();
  const requested = params.get("route_view");
  const mode = views.some(v => v.value === requested) ? requested : initialView === "egress" ? "exit" : initialView === "forwards" ? "ports" : "forward";
  const exitType = params.get("exit_type") === "existing" || (initialView === "egress" && !params.has("exit_type")) ? "existing" : "managed";
  const navigate = (key: string, value: string) => setParams(previous => { const next = new URLSearchParams(previous); next.set("tab", "routes"); next.set("route_view", mode!); next.set(key, value); if (key === "route_view" && value === "exit" && initialView === "egress") next.set("exit_type", exitType); return next; });
  const inventory = useQuery({ queryKey: ["server-network", server.id], queryFn: () => get<NetworkView>(`/api/v1/servers/${server.id}/network`), refetchInterval: 10000 });
  return <div className="mt-5 grid gap-5 lg:items-stretch lg:grid-cols-[224px_minmax(0,1fr)]">
    <nav aria-label="线路功能" className="grid content-start grid-cols-2 gap-1.5 rounded-xl border bg-card p-2 lg:auto-rows-fr lg:content-stretch lg:grid-cols-1">
      {views.map(({value, title, description, icon: Icon}) => <button key={value} type="button" aria-current={mode === value ? "page" : undefined} onClick={() => navigate("route_view", value)} className={`flex items-start gap-3 rounded-lg lg:items-center border px-3 py-2.5 text-left transition-colors focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-primary/30 ${mode === value ? "border-primary/25 bg-primary/10 text-primary" : "border-transparent hover:bg-muted"}`}>
        <Icon className="mt-0.5 h-4 w-4 shrink-0 lg:mt-0" aria-hidden="true" />
        <span><span className="block text-sm font-semibold">{title}</span><span className="mt-1 hidden text-xs leading-relaxed text-muted-foreground sm:block">{description}</span></span>
      </button>)}
    </nav>
    <div className="min-w-0 space-y-4 lg:flex lg:flex-col lg:gap-4 lg:space-y-0 lg:*:last:flex-1">
      {mode === "forward" && <AccessRoutes key={server.id} server={server} />}
      {mode === "ports" && <ServerForwards key={server.id} server={server} standaloneOnly stretch showOperations={false} />}
      {mode === "history" && <NetworkOperations serverID={server.id} />}
      {mode === "exit" && <>
        <div className="rounded-xl border bg-card p-5">
          <h2 className="font-semibold">为已有节点更换出口</h2>
          <p className="mt-1 mb-4 text-sm text-muted-foreground">客户端仍连接原节点，只改变节点从哪里访问网站。</p>
          <div className="max-w-xl"><Field label="出口方式"><Select aria-label="出口方式" value={exitType} onChange={e => navigate("exit_type", e.target.value)}><option value="managed">另一台受管 VPS · 自动配置两端</option><option value="existing">已有代理或本机网卡 · 手动配置出口</option></Select></Field></div>
        </div>
        {exitType === "managed" ? <ManagedTransits key={server.id} server={server} inventory={inventory.data} /> : <ServerEgress key={server.id} server={server} inlineBinding showOperations={false} />}
      </>}
    </div>
  </div>;
}
