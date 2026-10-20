import type { Server } from "./types";

const failureDescriptions: Record<string, string> = {
  ingress_policy_unsupported: "服务器的防火墙规则暂不兼容。请检查主机防火墙和节点端口，查看诊断后重试。",
  proxy_mark_unavailable: "服务器的路由能力检查未通过。请检查宿主内核和容器权限；重新安装 agent 不会改变这些条件。",
  proxy_sandbox_unavailable: "服务器无法启用所需的进程隔离。请检查宿主内核或容器环境，保留隔离保护后再重试。",
  proxy_cgroup_mismatch: "代理进程未在要求的资源隔离组内运行。请查看诊断，检查服务定义是否被覆盖。",
  proxy_preflight_failed: "代理配置预检未通过。请查看本机本次预检日志，修复配置或运行环境后重试。",
};

export function configurationState(s: Server, retrying = false) {
  if (!s.desired || s.desired.in_sync) return null;
  if (s.agent_status !== "online") return { kind: "waiting", title: s.agent_status === "pending" ? "等待服务器接入" : "等待服务器上线", description: "节点设置已保存，服务器恢复连接后会自动同步，无需重复安装 agent。" };
  if (s.diagnostics?.security_paused || s.diagnostics?.security_policy === false) return { kind: "blocked", title: "服务器已暂停配置变更", description: "请先检查服务器的本机安全策略，恢复后再同步节点设置。" };
  if (retrying || s.desired.status === "pending") return { kind: "syncing", title: "正在同步节点设置", description: "正在等待服务器确认结果，请稍候。" };
  const error = s.desired.error || s.agent?.apply_error;
  if (error) {
    const code = error.match(/\[(ingress_policy_unsupported|proxy_mark_unavailable|proxy_sandbox_unavailable|proxy_cgroup_mismatch|proxy_preflight_failed)\]/)?.[1];
    const description = code ? failureDescriptions[code]
      : /address already in use|端口.*占用/i.test(error) ? "节点端口被其他程序占用。请修改节点端口或停止占用程序，然后重试。"
      : /no space left|磁盘.*不足/i.test(error) ? "服务器磁盘空间不足。请清理空间后重试。"
      : /timeout|timed out|deadline exceeded|connection refused|network is unreachable/i.test(error) ? "服务器连接超时或网络暂时不可用。请检查网络后重试。"
      : /certificate|acme|证书/i.test(error) ? "证书配置未完成。请检查节点域名解析和证书设置，查看详情确认原因。"
      : "服务器未能完成节点配置。请查看错误详情，处理后重试；重新安装 agent 不一定能解决此问题。";
    return { kind: "failed", title: "节点设置未生效", description, error };
  }
  return { kind: "syncing", title: "正在同步节点设置", description: "设置已保存，等待服务器接收并确认。" };
}
