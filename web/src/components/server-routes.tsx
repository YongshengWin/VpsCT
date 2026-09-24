import { useQuery } from "@tanstack/react-query";
import { Activity, ArrowRight, ArrowRightLeft, ChevronRight, Network } from "lucide-react";
import { ManagedTransits } from "@/components/managed-transits";
import { get } from "@/lib/api";
import type { NetworkView, Server } from "@/lib/types";

type DetailView = "network" | "egress" | "forwards";

const otherActions: { view: DetailView; title: string; description: string; icon: typeof Activity }[] = [
  { view: "network", title: "看网卡和流量", description: "看这台 VPS 如何联网，配额按哪张网卡计算。", icon: Activity },
  { view: "egress", title: "连接已有代理", description: "已有 SOCKS5、SSH 等服务时，让节点从那里出网。", icon: ArrowRightLeft },
  { view: "forwards", title: "转发固定端口", description: "把访问这台 VPS 某个端口的连接送到指定目标。", icon: Network },
];

export function ServerRoutes({ server, onNavigate }: { server: Server; onNavigate: (view: DetailView) => void }) {
  const inventory = useQuery({
    queryKey: ["server-network", server.id],
    queryFn: () => get<NetworkView>(`/api/v1/servers/${server.id}/network`),
    refetchInterval: 10000,
  });

  return <div className="mt-5 space-y-5">
    <section className="rounded-xl border bg-card p-5" aria-labelledby="route-title">
      <p className="text-xs font-medium text-muted-foreground">节点访问路径</p>
      <h2 id="route-title" className="mt-1 text-lg font-semibold">网站会看到哪台 VPS 的地址？</h2>
      <div className="mt-4 flex flex-wrap items-center gap-2 text-sm">
        <span className="rounded-md bg-muted px-3 py-2">用户设备</span>
        <ArrowRight className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
        <span className="rounded-md border border-primary/40 bg-primary/5 px-3 py-2 font-medium">{server.name} 的节点</span>
        <ArrowRight className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
        <span className="rounded-md border border-dashed px-3 py-2">另一台 VPS（设置中转时）</span>
        <ArrowRight className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
        <span className="rounded-md bg-muted px-3 py-2">网站</span>
      </div>
      <p className="mt-3 text-sm text-muted-foreground">不设置中转时，节点直接从 {server.name} 访问网站。</p>
    </section>

    <ManagedTransits server={server} inventory={inventory.data} />

    <section aria-labelledby="other-network-actions">
      <h3 id="other-network-actions" className="mb-3 text-sm font-semibold">其他网络操作</h3>
      <div className="grid gap-3 md:grid-cols-3">
        {otherActions.map(({ view, title, description, icon: Icon }) => <button key={view} type="button" onClick={() => onNavigate(view)} className="group flex h-full min-h-36 flex-col rounded-xl border bg-card p-4 text-left transition-colors hover:border-primary/40 hover:bg-muted/30 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
          <span className="mb-4 flex h-9 w-9 items-center justify-center rounded-lg bg-muted text-muted-foreground group-hover:text-foreground"><Icon className="h-4 w-4" aria-hidden="true" /></span>
          <span className="flex w-full items-center justify-between gap-2 font-medium">{title}<ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" /></span>
          <span className="mt-1 text-sm text-muted-foreground">{description}</span>
        </button>)}
      </div>
    </section>
  </div>;
}
