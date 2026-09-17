import { AlertTriangle, Clock3, RefreshCw } from "lucide-react";
import type { Server } from "@/lib/types";
import { Button } from "@/components/ui";

export function configurationState(s: Server, retrying = false) {
  if (!s.desired || s.desired.in_sync) return null;
  if (s.agent_status !== "online") return { kind: "waiting", title: s.agent_status === "pending" ? "等待服务器接入" : "等待服务器上线", description: "节点设置已保存，服务器恢复连接后会自动同步，无需重复安装 agent。" };
  if (s.diagnostics?.security_paused || s.diagnostics?.security_policy === false) return { kind: "blocked", title: "服务器已暂停配置变更", description: "请先检查服务器的本机安全策略，恢复后再同步节点设置。" };
  if (retrying || s.desired.status === "pending") return { kind: "syncing", title: "正在同步节点设置", description: "正在等待服务器确认结果，请稍候。" };
  const error = s.desired.error || s.agent?.apply_error;
  if (error) {
    const description = /address already in use|端口.*占用/i.test(error) ? "节点端口被其他程序占用。请修改节点端口或停止占用程序，然后重试。"
      : /no space left|磁盘.*不足/i.test(error) ? "服务器磁盘空间不足。请清理空间后重试。"
      : /timeout|timed out|deadline exceeded|connection refused|network is unreachable/i.test(error) ? "服务器连接超时或网络暂时不可用。请检查网络后重试。"
      : /certificate|acme|证书/i.test(error) ? "证书配置未完成。请检查节点域名解析和证书设置，查看详情确认原因。"
      : "服务器未能完成节点配置。请查看错误详情，处理后重试；重新安装 agent 不一定能解决此问题。";
    return { kind: "failed", title: "节点设置未生效", description, error };
  }
  return { kind: "syncing", title: "正在同步节点设置", description: "设置已保存，等待服务器接收并确认。" };
}

export function ConfigStatusNotice({ server, retrying, disabled, onRetry, onDetails }: { server: Server; retrying?: boolean; disabled?: boolean; onRetry: () => void; onDetails: () => void }) {
  const state = configurationState(server, retrying);
  if (!state) return null;
  const failure = state.kind === "failed" || state.kind === "blocked";
  const Icon = failure ? AlertTriangle : state.kind === "syncing" ? RefreshCw : Clock3;
  return <div role="status" className={`mb-4 flex flex-wrap items-start justify-between gap-3 rounded-xl border p-4 ${failure ? "border-amber-500/30 bg-amber-500/5" : "border-sky-500/20 bg-sky-500/5"}`}>
    <div className="flex min-w-0 flex-1 gap-3"><Icon className={`mt-0.5 h-5 w-5 shrink-0 ${failure ? "text-amber-600" : "text-sky-600"} ${state.kind === "syncing" ? "animate-spin" : ""}`} /><div><p className="text-sm font-semibold">{state.title}</p><p className="mt-1 text-sm text-muted-foreground">{state.description}</p>
    {state.kind === "failed" && <details className="mt-2 text-xs"><summary className="cursor-pointer text-muted-foreground">错误详情</summary><p className="mt-2 whitespace-pre-wrap break-all">{state.error}</p><p className="mt-2 text-muted-foreground">重试会同步这台服务器的全部节点设置，保留现有节点密码。</p></details>}
    </div></div>
    {failure && <div className="flex gap-2"><Button size="sm" variant="ghost" onClick={onDetails}>查看诊断</Button>{state.kind === "failed" && <Button size="sm" variant="outline" disabled={disabled || retrying} onClick={onRetry}>重试</Button>}</div>}
  </div>;
}
