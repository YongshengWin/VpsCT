import type * as React from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { ChevronRight } from "lucide-react";
import { get } from "@/lib/api";
import { useIsAdmin } from "@/lib/auth";
import type { Server, Series, Share, Subscription } from "@/lib/types";
import { cn, fmtBytes } from "@/lib/utils";
import { nicIO } from "@/components/traffic-ways";
import { Badge, Button, Card, Empty, Progress, SectionTitle, Spinner } from "@/components/ui";
import { TrafficBars } from "@/components/charts";
import { ShareCard } from "@/pages/shares";
import { SubscriptionLinks } from "@/pages/subscriptions";

interface AdminDash {
  counts: { servers: number; servers_online: number; nodes: number; shares: number; shares_active: number; subscriptions: number; externals: number };
  traffic: { servers_30d: Series; externals_30d: Series; today_up: number; today_down: number; month_up: number; month_down: number };
  alerts: { level: string; kind: string; message: string; server_id?: number; share_id?: number }[];
  servers: Server[];
}
interface UserDash {
  shares: Share[];
  subscriptions: Subscription[];
}

export function DashboardPage() {
  const admin = useIsAdmin();
  const q = useQuery({ queryKey: ["dashboard"], queryFn: () => get<AdminDash & UserDash>("/api/v1/dashboard"), refetchInterval: 30000 });
  if (q.isLoading) return <Spinner />;
  if (!q.data) return <Card role="alert" className="space-y-3 p-6">
    <h1 className="text-lg font-semibold">总览暂时无法加载</h1>
    <p className="text-sm text-muted-foreground">{q.error instanceof Error ? q.error.message : "暂时未能获取总览数据，请重试。"}</p>
    <Button onClick={() => void q.refetch()} loading={q.isFetching}>重新加载</Button>
  </Card>;
  return admin ? <AdminDashboard d={q.data} /> : <UserDashboard d={q.data} />;
}

// ---------- building blocks ----------

function Kpi({ label, value, of, to, hint }: { label: string; value: React.ReactNode; of?: number; to?: string; hint?: string }) {
  const body = (
    <Card className={cn("h-full px-4 py-3.5", to && "transition-colors hover:bg-accent/40")}>
      <p className="text-xs font-medium text-muted-foreground">{label}</p>
      <p className="mt-1.5 truncate text-2xl font-bold tabular-nums leading-none">
        {value}
        {of !== undefined && <span className="text-sm font-medium text-muted-foreground"> / {of}</span>}
      </p>
      {hint && <p className="mt-1.5 truncate text-xs text-muted-foreground">{hint}</p>}
    </Card>
  );
  return to ? (
    <Link to={to} className="block min-w-0">
      {body}
    </Link>
  ) : (
    body
  );
}

function Figure({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="mt-0.5 text-sm font-semibold tabular-nums">{value}</dd>
    </div>
  );
}

function ServerRow({ s }: { s: Server }) {
  const online = s.agent_status === "online";
  const memPct = s.metrics?.mem_total ? (s.metrics.mem_used / s.metrics.mem_total) * 100 : 0;
  const meta = [s.region, `${s.node_count} 节点`, online && s.metrics ? `CPU ${s.metrics.cpu_percent.toFixed(0)}% · 内存 ${memPct.toFixed(0)}%` : null].filter(Boolean).join(" · ");
  const billed = s.usage?.billed ?? 0;
  const io = nicIO(s.usage?.inbound ?? s.usage?.up, s.usage?.outbound ?? s.usage?.down);
  return (
    <Link to={`/servers/${s.id}`} className="flex items-center gap-3 px-5 py-3 transition-colors hover:bg-accent/50">
      <span
        className={cn("h-2 w-2 shrink-0 rounded-full", online ? "bg-emerald-500" : s.agent_status === "offline" ? "bg-rose-500" : "bg-muted-foreground/40")}
        title={online ? "在线" : s.agent_status === "offline" ? "离线" : "未连接"}
      />
      <div className="min-w-0 flex-1">
        <p className="break-words text-sm font-semibold">{s.name}</p>
        <p className="break-words text-xs text-muted-foreground">{meta}</p>
      </div>
      <div className="w-44 shrink-0 text-right">
        <p className="text-xs font-medium tabular-nums">汇总 {fmtBytes(s.usage?.total ?? io.total)}</p>
        <p className="mt-0.5 text-[11px] text-muted-foreground tabular-nums">入 {fmtBytes(io.inbound)} · 出 {fmtBytes(io.outbound)}</p>
        {s.quota_bytes > 0 && (
          <p className="mt-0.5 text-[11px] text-muted-foreground tabular-nums">
            配额 {fmtBytes(billed)} / {fmtBytes(s.quota_bytes, 0)}
          </p>
        )}
        {s.quota_bytes > 0 && s.usage && <Progress value={s.usage.percent} className="mt-1.5 h-1.5" />}
      </div>
    </Link>
  );
}

function Step({ n, to, title, desc }: { n: number; to: string; title: string; desc: string }) {
  return (
    <li>
      <Link to={to} className="group flex items-center gap-3 rounded-xl px-3 py-2.5 transition-colors hover:bg-accent/60">
        <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-primary/10 text-xs font-bold text-primary">{n}</span>
        <div className="min-w-0 flex-1">
          <p className="text-sm font-semibold">{title}</p>
          <p className="text-xs text-muted-foreground">{desc}</p>
        </div>
        <ChevronRight className="h-4 w-4 text-muted-foreground/60 transition-transform group-hover:translate-x-0.5" />
      </Link>
    </li>
  );
}

function MoreLink({ to }: { to: string }) {
  return (
    <Link to={to} className="inline-flex items-center gap-0.5 text-xs font-medium text-muted-foreground hover:text-foreground">
      全部 <ChevronRight className="h-3.5 w-3.5" />
    </Link>
  );
}

// ---------- admin ----------

function AdminDashboard({ d }: { d: AdminDash }) {
  const c = d.counts;
  const t = d.traffic;
  const monthIO = nicIO(t.month_up, t.month_down);
  const todayIO = nicIO(t.today_up, t.today_down);
  const points = t.servers_30d?.points ?? [];
  const hasTraffic = points.some((p) => p.up + p.down > 0);
  const fresh = c.servers === 0 && c.nodes === 0 && c.externals === 0;

  return (
    <div className="space-y-4">
      {/* key numbers */}
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
        <Kpi to="/servers" label="服务器在线" value={c.servers_online} of={c.servers} />
        <Kpi to="/nodes" label="节点" value={c.nodes} />
        <Kpi to="/shares" label="分享活跃" value={c.shares_active} of={c.shares} />
        <Kpi to="/subscriptions" label="订阅链接" value={c.subscriptions} />
        <Kpi to="/externals" label="外部订阅" value={c.externals} />
        <Kpi label="今日流量（汇总）" value={fmtBytes(todayIO.total)} hint={`入 ${fmtBytes(todayIO.inbound)} · 出 ${fmtBytes(todayIO.outbound)}`} />
      </div>

      {/* alerts: only when there is something to act on */}
      {d.alerts.length > 0 && (
        <Card className="divide-y divide-border/60 overflow-hidden border-rose-200/80 dark:divide-border dark:border-rose-500/30">
          {d.alerts.map((a, i) => {
            const to = a.server_id ? `/servers/${a.server_id}` : a.share_id ? `/shares/${a.share_id}` : undefined;
            const row = (
              <>
                <Badge variant={a.level === "error" ? "destructive" : a.level === "warn" ? "warning" : "info"} className="shrink-0">
                  {a.level}
                </Badge>
                <span className="min-w-0 flex-1 break-words text-sm">{a.message}</span>
                {to && <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground/60" />}
              </>
            );
            const cls = "flex items-center gap-3 px-5 py-2.5";
            return to ? (
              <Link key={i} to={to} className={cn(cls, "transition-colors hover:bg-accent/50")}>
                {row}
              </Link>
            ) : (
              <div key={i} className={cls}>
                {row}
              </div>
            );
          })}
        </Card>
      )}

      <Card className="p-5">
        <div className="flex flex-wrap items-end justify-between gap-x-8 gap-y-3">
          <div>
            <p className="text-xs font-medium text-muted-foreground">本月流量（汇总）</p>
            <p className="mt-1.5 text-3xl font-bold tabular-nums leading-none">{fmtBytes(monthIO.total)}</p>
            <p className="mt-1.5 text-xs text-muted-foreground">入站 + 出站</p>
          </div>
          <dl className="flex flex-wrap gap-6">
            <Figure label="入站" value={fmtBytes(monthIO.inbound)} />
            <Figure label="出站" value={fmtBytes(monthIO.outbound)} />
            <Figure label="今日汇总" value={fmtBytes(todayIO.total)} />
            <Figure label="今日入站" value={fmtBytes(todayIO.inbound)} />
            <Figure label="今日出站" value={fmtBytes(todayIO.outbound)} />
          </dl>
        </div>
        <div className="mt-5">
          {hasTraffic ? (
            <TrafficBars points={points} height={220} />
          ) : (
            <div className="flex h-[160px] items-center justify-center rounded-xl bg-muted/50 text-sm text-muted-foreground">近 30 天暂无流量数据</div>
          )}
        </div>
      </Card>

      <Card className="overflow-hidden">
        <div className="flex items-center justify-between border-b border-border/60 px-5 py-3.5 dark:border-border">
          <h2 className="text-sm font-bold">服务器</h2>
          <MoreLink to="/servers" />
        </div>
        {d.servers.length === 0 ? (
          <div className="flex flex-col items-center justify-center px-5 py-12 text-center">
            <p className="text-sm text-muted-foreground">还没有服务器</p>
            <Link to="/servers" className="mt-4 inline-flex h-9 items-center rounded-full bg-primary px-4 text-sm font-semibold text-primary-foreground hover:bg-primary/90">
              添加服务器
            </Link>
          </div>
        ) : (
          <div className="grid md:grid-cols-2">
            {d.servers.slice(0, 8).map((s) => (
              <div key={s.id} className="border-b border-border/60 dark:border-border md:odd:border-r">
                <ServerRow s={s} />
              </div>
            ))}
          </div>
        )}
      </Card>

      {/* first-run guide: disappears once anything exists */}
      {fresh && (
        <Card className="p-3 sm:p-4">
          <p className="px-3 pb-1 pt-1 text-xs font-medium text-muted-foreground">开始使用</p>
          <ol className="grid gap-1 sm:grid-cols-3">
            <Step n={1} to="/servers" title="添加服务器" desc="一条命令安装 agent" />
            <Step n={2} to="/nodes" title="创建节点" desc="或从外部订阅导入" />
            <Step n={3} to="/subscriptions/new" title="生成订阅" desc="或创建带配额的分享" />
          </ol>
        </Card>
      )}
    </div>
  );
}

// ---------- normal user ----------

function UserDashboard({ d }: { d: UserDash }) {
  return (
    <div className="space-y-8">
      <section>
        <SectionTitle>分享</SectionTitle>
        {d.shares.length === 0 ? (
          <Empty title="暂无分享" description="管理员创建分享后会显示在这里。" />
        ) : (
          <div className="grid gap-4 md:grid-cols-2">
            {d.shares.map((s) => (
              <ShareCard key={s.id} share={s} />
            ))}
          </div>
        )}
      </section>

      {d.subscriptions.length > 0 && (
        <section>
          <SectionTitle>订阅链接</SectionTitle>
          <div className="space-y-4">
            {d.subscriptions.map((s) => (
              <Card key={s.id} className="p-5">
                <p className="font-semibold">{s.name}</p>
                <SubscriptionLinks sub={s} />
              </Card>
            ))}
          </div>
        </section>
      )}
    </div>
  );
}
