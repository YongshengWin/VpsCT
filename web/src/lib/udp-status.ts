export interface UDPStatus {
  state: "inactive" | "starting" | "healthy" | "degraded" | "paused";
  code?: string;
  updated_at: string;
  last_healthy: string;
  active?: Record<string, number>;
  reaped: number;
  sweep_millis: number;
}

export function udpStatusView(status: UDPStatus | undefined, online: boolean, now = Date.now()) {
  if (!online) return { warning: true, title: "UDP 状态待确认", description: "服务器离线，历史会话数不能代表当前运行状态。", fresh: false };
  if (!status || !Number.isFinite(Date.parse(status.updated_at)) || now - Date.parse(status.updated_at) > 45000 || Date.parse(status.updated_at) - now > 45000) return { warning: true, title: "等待 UDP 运行诊断", description: "等待 agent 上报最新状态，尚不能确认 UDP 转发可用。", fresh: false };
  const reason = status.code === "udp_conntrack_unavailable" ? "内核会话观察不可用" : status.code === "udp_guard_unavailable" ? "UDP 健康保护规则未就绪" : status.code === "udp_core_unavailable" ? "本机代理会话接口未就绪" : "本机代理会话接口读取或回收失败";
  const scope = status.code === "udp_guard_unavailable" || status.code === "udp_core_unavailable" ? "代理运行状态还需查看服务器诊断，系统将重试本机保护检查。" : "TCP 与普通代理节点继续运行，恢复后自动清理残留会话并重开 UDP。";
  switch (status.state) {
    case "healthy": return { warning: false, title: "UDP 会话管理正常", description: "空闲会话回收与 UDP 健康保护正在运行。", fresh: true };
    case "paused": return { warning: true, title: "UDP 转发已暂停", description: `${reason}，已关闭 UDP 通路。${scope}`, fresh: true };
    case "degraded": return { warning: true, title: "UDP 会话管理正在重试", description: `${reason}；健康租约到期会自动关闭 UDP 通路。`, fresh: true };
    case "inactive": return { warning: false, title: "UDP 会话管理未启用", description: "当前没有正在运行的 UDP 固定转发。", fresh: true };
    default: return { warning: false, title: "UDP 会话管理准备中", description: "等待本机检查完成后开放 UDP 通路。", fresh: false };
  }
}
