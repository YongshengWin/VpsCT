import { AlertTriangle, Clock3, RefreshCw } from "lucide-react";
import type { Server } from "@/lib/types";
import { Button } from "@/components/ui";

import { configurationState } from "@/lib/config-status";
export { configurationState } from "@/lib/config-status";

export function ConfigStatusNotice({ server, retrying, disabled, onRetry, onDetails }: { server: Server; retrying?: boolean; disabled?: boolean; onRetry: () => void; onDetails: () => void }) {
  const state = configurationState(server, retrying);
  if (!state) return null;
  const failure = state.kind === "failed" || state.kind === "blocked";
  const Icon = failure ? AlertTriangle : state.kind === "syncing" ? RefreshCw : Clock3;
  return <div role="status" className={`mb-4 flex flex-col items-start gap-3 rounded-xl border p-4 sm:flex-row sm:justify-between ${failure ? "border-amber-500/30 bg-amber-500/5" : "border-sky-500/20 bg-sky-500/5"}`}>
    <div className="flex w-full min-w-0 flex-1 gap-3"><Icon className={`mt-0.5 h-5 w-5 shrink-0 ${failure ? "text-amber-600" : "text-sky-600"} ${state.kind === "syncing" ? "animate-spin" : ""}`} /><div className="min-w-0"><p className="text-sm font-semibold">{state.title}</p><p className="mt-1 text-sm text-muted-foreground">{state.description}</p>
    {state.kind === "failed" && <details className="mt-2 text-xs"><summary className="cursor-pointer text-muted-foreground">错误详情</summary><p className="mt-2 whitespace-pre-wrap break-all">{state.error}</p><p className="mt-2 text-muted-foreground">重试会同步这台服务器的全部节点设置，保留现有节点密码。</p></details>}
    </div></div>
    {failure && <div className="flex w-full justify-end gap-2 sm:w-auto sm:shrink-0"><Button size="sm" variant="ghost" onClick={onDetails}>查看诊断</Button>{state.kind === "failed" && <Button size="sm" variant="outline" disabled={disabled || retrying} onClick={onRetry}>重试</Button>}</div>}
  </div>;
}
