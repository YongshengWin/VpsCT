export type NATNetwork = "tcp" | "udp" | "both";
export interface NATMapping { public_port: number; local_port: number; count?: number; network?: NATNetwork }
export interface NATConfig { schema: 1; interface?: string; local_address?: string; mappings: NATMapping[] }
export interface NATReport {
  version: number;
  source: "none" | "local" | "controller";
  configured: boolean;
  status: "disabled" | "pending" | "applied" | "error";
  config?: NATConfig;
  error?: string;
}
export interface NATSummary {
  supported: boolean;
  source: "unknown" | NATReport["source"];
  configured: boolean;
  status: "unknown" | NATReport["status"];
  editable: boolean;
  reason?: string;
  error?: string;
  pending: boolean;
  revision: number;
}
export interface NATView {
  server_id: number;
  revision: number;
  desired_config: NATConfig | null;
  report: NATReport | null;
  editable: boolean;
  reason?: string;
  pending: boolean;
}

export function hasNAT(nat?: NATSummary) {
  return !!nat && (nat.configured || nat.pending || (nat.source === "local" && nat.status === "error"));
}

export function natState(agentStatus: string, nat?: NATSummary, view?: NATView) {
  if (agentStatus !== "online") return { label: agentStatus === "pending" ? "等待 agent 接入" : "离线，等待确认", variant: "secondary" as const };
  if (!nat?.supported) return { label: "需要更新 agent", variant: "secondary" as const };
  // Summary and detail refresh independently. Require both to agree before
  // showing success; either may already know about a newer pending revision.
  if (view?.pending || nat.pending) return { label: "等待 agent 应用", variant: "warning" as const };
  const statuses = view ? [nat.status, view.report?.status] : [nat.status];
  if (statuses.includes("error")) return { label: "应用失败", variant: "destructive" as const };
  if (statuses.every((s) => s === "applied")) return { label: "本机已应用", variant: "success" as const };
  if (statuses.every((s) => s === "disabled")) return { label: "未启用", variant: "secondary" as const };
  if (statuses.includes("pending")) return { label: "等待 agent 应用", variant: "warning" as const };
  return { label: "等待 agent 上报", variant: "secondary" as const };
}

export function natConfigForDeploy(view?: NATView): NATConfig | undefined {
  // A local root file is authoritative, including a malformed/empty file.
  if (view?.report?.source === "local") return view.report.config;
  return view?.desired_config ?? view?.report?.config;
}

export function natTransports(protocol: string, mieruTransport = "TCP", ssNetwork = "both"): ("tcp" | "udp")[] {
  if (["hysteria2", "tuic", "wireguard"].includes(protocol)) return ["udp"];
  if (["ss", "shadowsocks"].includes(protocol)) return ssNetwork === "tcp" ? ["tcp"] : ["tcp", "udp"];
  if (protocol === "mieru") return [mieruTransport === "UDP" ? "udp" : "tcp"];
  return ["tcp"];
}

export function natCovers(config: NATConfig, protocol: string, port?: number, mieruTransport = "TCP", ssNetwork = "both") {
  const transports = natTransports(protocol, mieruTransport, ssNetwork);
  const ports = new Map<number, Set<string>>();
  for (const m of config.mappings) {
    for (let offset = 0; offset < (m.count || 1); offset++) {
      const p = m.public_port + offset;
      if (port !== undefined && p !== port) continue;
      const networks = ports.get(p) ?? new Set<string>();
      for (const network of m.network === "tcp" ? ["tcp"] : m.network === "udp" ? ["udp"] : ["tcp", "udp"]) networks.add(network);
      ports.set(p, networks);
    }
  }
  return [...ports.values()].some((networks) => transports.every((n) => networks.has(n)));
}

export interface NATDraft {
  interface: string;
  local_address: string;
  mappings: { public_port: string; local_port: string; count: string; network: NATNetwork }[];
}
export const emptyNATMapping = () => ({ public_port: "", local_port: "", count: "1", network: "both" as const });
export function natDraft(config?: NATConfig | null): NATDraft {
  return {
    interface: config?.interface ?? "", local_address: config?.local_address ?? "",
    mappings: config?.mappings.length ? config.mappings.map((m) => ({ public_port: String(m.public_port), local_port: String(m.local_port), count: String(m.count || 1), network: m.network || "both" })) : [emptyNATMapping()],
  };
}

export function natDraftConfig(draft: NATDraft): NATConfig {
  const integer = (s: string, label: string) => {
    if (!/^\d+$/.test(s) || !Number.isSafeInteger(Number(s)) || Number(s) < 1) throw new Error(`${label}需填写正整数`);
    return Number(s);
  };
  const iface = draft.interface.trim(), address = draft.local_address.trim();
  if (iface && (!/^[a-zA-Z0-9_.-]{1,15}$/.test(iface) || iface === "." || iface === "..")) throw new Error("网卡名称需为 1–15 个字母、数字、点、下划线或连字符，不能为 . 或 ..");
  if (address) {
    const parts = address.split(".");
    const n = parts.map(Number);
    if (parts.length !== 4 || parts.some((p) => !/^(0|[1-9]\d{0,2})$/.test(p)) || n.some((p) => p > 255) || n[0] === 127 || (n[0] >= 224 && n[0] <= 239) || address === "0.0.0.0" || address === "255.255.255.255") throw new Error("本机地址需为非回环 IPv4 单播地址");
  }
  if (!draft.mappings.length) throw new Error("请添加至少一条端口映射");
  let total = 0;
  const publicPorts = new Set<string>(), localPorts = new Set<string>();
  const mappings = draft.mappings.map((m, i) => {
    const label = `第 ${i + 1} 条映射`;
    const pub = integer(m.public_port, `${label}公网端口`), local = integer(m.local_port, `${label}本机端口`), count = integer(m.count, `${label}数量`);
    if (pub + count - 1 > 65535 || local + count - 1 > 65535) throw new Error(`${label}端口范围不能超过 65535`);
    if ((total += count) > 4096) throw new Error("展开后最多允许 4096 个端口对");
    if (pub !== local && local < 1024) throw new Error(`${label}不能转换本机 1–1023 端口`);
    const transports = m.network === "both" ? ["tcp", "udp"] : [m.network];
    for (let offset = 0; offset < count; offset++) for (const transport of transports) {
      const p = `${transport}:${pub + offset}`, l = `${transport}:${local + offset}`;
      if (publicPorts.has(p) || localPorts.has(l)) throw new Error(`${label}与其他映射的同协议端口范围重叠`);
      publicPorts.add(p); localPorts.add(l);
    }
    return { public_port: pub, local_port: local, count, network: m.network };
  });
  return { schema: 1, ...(iface ? { interface: iface } : {}), ...(address ? { local_address: address } : {}), mappings };
}

export const natPortRange = (start: number, count = 1) => count > 1 ? `${start}–${start + count - 1}` : String(start);

export function isNATServer(server: { is_nat?: boolean; nat?: NATSummary }) {
  return !!server.is_nat || hasNAT(server.nat);
}
