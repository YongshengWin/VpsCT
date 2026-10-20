import { cn, fmtBytes } from "@/lib/utils";
import { trafficIO } from "@/lib/traffic-billing";

/** Compatibility alias. Direction counters belong to the current subject. */
export const nicIO = trafficIO;

export function TrafficIO({
  inbound = 0,
  outbound = 0,
  className,
  compact,
}: {
  inbound?: number;
  outbound?: number;
  className?: string;
  compact?: boolean;
}) {
  const io = trafficIO(inbound, outbound);
  if (compact) {
    return (
      <p className={cn("text-xs tabular-nums text-muted-foreground", className)}>
        入站 {fmtBytes(io.inbound)}
        <span className="mx-1.5 opacity-40">·</span>
        出站 {fmtBytes(io.outbound)}
        <span className="mx-1.5 opacity-40">·</span>
        <span className="font-medium text-foreground">汇总 {fmtBytes(io.total)}</span>
      </p>
    );
  }
  return (
    <div className={cn("grid grid-cols-3 gap-2 text-sm", className)}>
      <div className="rounded-md bg-muted/50 p-2">
        <p className="text-xs text-muted-foreground">入站</p>
        <p className="font-semibold tabular-nums">{fmtBytes(io.inbound)}</p>
      </div>
      <div className="rounded-md bg-muted/50 p-2">
        <p className="text-xs text-muted-foreground">出站</p>
        <p className="font-semibold tabular-nums">{fmtBytes(io.outbound)}</p>
      </div>
      <div className="rounded-md bg-primary/5 p-2">
        <p className="text-xs text-muted-foreground">汇总 · 入+出</p>
        <p className="font-semibold tabular-nums">{fmtBytes(io.total)}</p>
      </div>
    </div>
  );
}
