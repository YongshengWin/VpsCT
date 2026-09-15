import { Area, AreaChart, Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis, Legend } from "recharts";
import type { TrafficPoint } from "@/lib/types";
import { fmtBytes } from "@/lib/utils";

function tickBytes(v: number) {
  return fmtBytes(v, 0);
}

function dayLabel(s: string) {
  const d = new Date(s);
  return `${d.getMonth() + 1}/${d.getDate()}`;
}

export function TrafficBars({ points, height = 220 }: { points: TrafficPoint[]; height?: number }) {
  const data = points.map((p) => ({ day: dayLabel(p.bucket), 入站: p.up, 出站: p.down }));
  return (
    <ResponsiveContainer width="100%" height={height}>
      <BarChart data={data} margin={{ top: 8, right: 8, left: 0, bottom: 0 }} barCategoryGap="30%">
        <CartesianGrid strokeDasharray="3 3" className="stroke-border" vertical={false} />
        <XAxis dataKey="day" tick={{ fontSize: 11, fill: "hsl(var(--muted-foreground))" }} tickLine={false} axisLine={false} interval="preserveStartEnd" minTickGap={24} />
        <YAxis tickFormatter={tickBytes} tick={{ fontSize: 11, fill: "hsl(var(--muted-foreground))" }} tickLine={false} axisLine={false} width={64} />
        <Tooltip
          formatter={(v: number) => fmtBytes(v)}
          contentStyle={{ background: "hsl(var(--card))", border: "1px solid hsl(var(--border))", borderRadius: 14, fontSize: 12, boxShadow: "0 8px 24px -8px rgba(16,24,40,.2)" }}
          cursor={{ fill: "hsl(var(--foreground) / 0.04)" }}
        />
        <Legend wrapperStyle={{ fontSize: 12 }} iconType="circle" iconSize={8} />
        <Bar dataKey="入站" stackId="a" fill="hsl(var(--primary))" radius={[0, 0, 0, 0]} />
        <Bar dataKey="出站" stackId="a" fill="hsl(var(--primary) / 0.4)" radius={[4, 4, 0, 0]} />
      </BarChart>
    </ResponsiveContainer>
  );
}

export function RateArea({ points, height = 200 }: { points: { ts: string; rx_rate: number; tx_rate: number }[]; height?: number }) {
  const data = points.map((p) => {
    const d = new Date(p.ts);
    return { t: `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`, 入站: p.rx_rate, 出站: p.tx_rate };
  });
  return (
    <ResponsiveContainer width="100%" height={height}>
      <AreaChart data={data} margin={{ top: 8, right: 8, left: 0, bottom: 0 }}>
        <defs>
          <linearGradient id="rx" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor="hsl(var(--primary))" stopOpacity={0.5} />
            <stop offset="100%" stopColor="hsl(var(--primary))" stopOpacity={0} />
          </linearGradient>
        </defs>
        <CartesianGrid strokeDasharray="3 3" className="stroke-border" vertical={false} />
        <XAxis dataKey="t" tick={{ fontSize: 11, fill: "hsl(var(--muted-foreground))" }} tickLine={false} axisLine={false} minTickGap={32} />
        <YAxis tickFormatter={(v: number) => fmtBytes(v, 0) + "/s"} tick={{ fontSize: 11, fill: "hsl(var(--muted-foreground))" }} tickLine={false} axisLine={false} width={80} />
        <Tooltip formatter={(v: number) => fmtBytes(v) + "/s"} contentStyle={{ background: "hsl(var(--card))", border: "1px solid hsl(var(--border))", borderRadius: 14, fontSize: 12, boxShadow: "0 8px 24px -8px rgba(16,24,40,.2)" }} />
        <Area type="monotone" dataKey="入站" stroke="hsl(var(--primary))" fill="url(#rx)" strokeWidth={2} />
        <Area type="monotone" dataKey="出站" stroke="#7c6cf0" fill="transparent" strokeWidth={2} />
      </AreaChart>
    </ResponsiveContainer>
  );
}
