import type { NetworkView, Node } from "./types";

export interface NetworkAddress { interface_id: string; address: string }
export interface DirectEgress {
  interface_id?: string;
  source_ipv4?: NetworkAddress;
  source_ipv6?: NetworkAddress;
  family: "dual" | "ipv4" | "ipv6";
  dns: { transport: "udp" | "tcp"; address: string; port: number };
}
export interface SOCKS5Egress {
  server: string;
  server_port: number;
  authentication: "none" | "password";
  udp: boolean;
  family: DirectEgress["family"];
  dns: DirectEgress["dns"];
  outer: DirectEgress;
  connect_timeout_seconds: number;
}
export interface SSHEgress extends Omit<SOCKS5Egress, "authentication"> { authentication: "password" | "private_key"; host_keys: string[] }
export interface SS2022Egress {
  server: string; server_port: number; method: "2022-blake3-aes-128-gcm" | "2022-blake3-aes-256-gcm";
  family: DirectEgress["family"]; dns: DirectEgress["dns"]; outer: DirectEgress; connect_timeout_seconds: number;
}
export interface WireGuardEgress {
  server: string; server_port: number; public_key: string; addresses: string[]; allowed_ips: string[];
  mtu: number; persistent_keepalive: number; reserved?: number[];
  family: DirectEgress["family"]; dns: DirectEgress["dns"]; outer: DirectEgress; connect_timeout_seconds: number;
}
export type EgressConfig = DirectEgress | SOCKS5Egress | SSHEgress | WireGuardEgress | SS2022Egress;
export interface SOCKS5Credentials { username: string; password: string; private_key?: string; private_key_passphrase?: string }
export function isWireGuard(config: EgressConfig): config is WireGuardEgress { return "public_key" in config; }
export function isSS2022(config: EgressConfig): config is SS2022Egress { return "method" in config; }
export function isUpstream(config: EgressConfig): config is SOCKS5Egress | SSHEgress { return "outer" in config && !isWireGuard(config); }
export function isSSH(config: EgressConfig): config is SSHEgress { return "host_keys" in config; }
export function isSOCKS5(config: EgressConfig): config is SOCKS5Egress { return isUpstream(config) && !isSSH(config); }
export function validSOCKS5Credentials(value: SOCKS5Credentials) {
  const encoder = new TextEncoder();
  return [value.username, value.password].every((v) => v.length > 0 && !v.includes("\u0000") && encoder.encode(v).length <= 255);
}
export function egressSummary(config: EgressConfig, inventory?: NetworkView) {
  if (isWireGuard(config)) return `WireGuard ${config.server}:${config.server_port} · ${interfaceName(inventory, config.outer.interface_id)} · ${config.family} · MTU ${config.mtu}`;
  if (isSS2022(config)) return `SS-2022 ${config.server}:${config.server_port} · ${interfaceName(inventory, config.outer.interface_id)} · ${config.family} · TCP + UDP`;
  const outer = isUpstream(config) ? config.outer : config;
  const prefix = isUpstream(config) ? `${isSSH(config) ? "SSH" : "SOCKS5"} ${config.server.includes(":") ? `[${config.server}]` : config.server}:${config.server_port} · ${config.udp ? "TCP + UDP" : "仅 TCP"} · 外层 ` : "直连 · ";
  return `${prefix}${interfaceName(inventory, outer.interface_id)} · 业务 ${config.family} · DNS ${config.dns.address}:${config.dns.port}`;
}
export interface NodeNetworkPolicy {
  listen_mode: "all" | "address";
  listen_address?: string;
  listen_interface_id?: string;
  advertise_mode: "inherit" | "override";
  egress_profile_id?: number;
  egress_revision?: number;
  on_unavailable: "block";
}
export interface EgressProfile {
 managed_stage?: string; id: number; server_id: number; name: string; kind: "direct" | "socks5" | "ssh" | "wireguard" | "ss2022"; enabled: boolean; current_revision: number }
export interface EgressRevision { profile_id: number; server_id: number; revision: number; config: EgressConfig; has_credentials?: boolean }
export interface EgressView {
  profile: EgressProfile;
  revision: EgressRevision;
  references: { node_id: number; name: string; enabled: boolean; revision: number }[];
  reference_count: number;
	forward_references?: NetworkImpactForward[];
	forward_reference_count?: number;
  limit: number;
  offset: number;
}
export interface NetworkReadiness {
	impact?: NetworkImpact;
  egress_version?: number;
	forward_version?: number;
  ready: boolean;
  architecture: string;
  pinned_core_version: string;
  installed_core_version: string;
  checks: { code: string; ready: boolean; message: string }[];
}
export interface NetworkImpact {
  token: string;
  server_id: number;
  server_enabled: boolean;
  runtime_change: boolean;
  reference_count: number;
  restart_count: number;
  restart_scope: string;
  history_complete: boolean;
  historical_nodes: { node_id: number; name: string; protocol: string; listen_port: number; share_id?: number }[];
	historical_forwards?: { forward_id: number; revision: number; listen_port: number }[];
	forwards?: NetworkImpactForward[];
  before_listen?: string;
  after_listen?: string;
  before_host?: string;
  after_host?: string;
  nodes: { node_id: number; name: string; protocol: string; core: string; listen_port: number; enabled: boolean; revoked: boolean; share_id?: number; share_name?: string; share_status?: string; network_revision: number; egress_profile_id?: number; egress_revision?: number; egress_enabled: boolean; blocked: boolean; effect: "binding" | "clear" | "disable" | "resume" | "pinned" | "restart"; restart_possible: boolean }[];
}
export interface EgressPreview { ready: boolean; issues: string[]; impact: NetworkImpact }
export interface NetworkOperation {
  id: string;
  server_id: number;
  kind: "node_network" | "egress_create" | "egress_update" | "egress_delete" | "forward_create" | "forward_update" | "forward_delete";
  resource_id: number;
  resource_revision: number;
  status: "saved" | "queued" | "publish_failed" | "waiting_agent" | "apply_failed" | "applied" | "superseded";
  retry_revision: number;
  desired_revision: number;
  message: string;
  created_at: string;
  updated_at: string;
}
export interface NetworkImpactForward { forward_id: number; name: string; revision: number; listen_port: number; enabled: boolean; retired: boolean; egress_profile_id?: number; egress_revision?: number; egress_enabled: boolean; effect: NetworkImpact["nodes"][number]["effect"]; restart_possible: boolean }
export interface ForwardConfig {
  listen_mode: "all" | "address";
  listen_address?: string;
  listen_interface_id?: string;
  listen_port: number;
  network: "tcp" | "udp" | "both";
  target_host: string;
  target_port: number;
  source_mode: "cidr" | "all";
  source_cidrs: string[];
  egress_profile_id?: number;
  egress_revision?: number;
  max_tcp_connections: number;
  max_udp_sessions: number;
  udp_idle_seconds: number;
}
export interface PortForward { rx_30_days: number; tx_30_days: number; reserved_ports: number[]; id: number; server_id: number; name: string; enabled: boolean; retired: boolean; revision: number; config: ForwardConfig }
export const networkStatus: Record<NetworkOperation["status"], string> = { saved: "已保存", queued: "等待发布", publish_failed: "发布失败", waiting_agent: "等待应用回执", apply_failed: "应用失败", applied: "已应用", superseded: "已被新变更替代" };
export const networkTerminal = (op?: NetworkOperation) => !!op && ["saved", "applied", "superseded"].includes(op.status);
export function canBindNodeNetwork(node: Pick<Node, "core" | "protocol" | "params">) {
  return (node.core === "snell" && node.protocol === "snell") || (node.core === "mieru" && node.protocol === "mieru") || node.core === "singbox" && (["vless", "trojan", "anytls", "hysteria2", "tuic"].includes(node.protocol) || (node.protocol === "ss" && node.params.cipher === "2022-blake3-aes-128-gcm"));
}
export function networkRequestUncertain(error: unknown) {
  return !!error && !(error instanceof Error && "status" in error && typeof error.status === "number" && error.status >= 400 && error.status < 500);
}
export function networkOperationID() { return Array.from(crypto.getRandomValues(new Uint8Array(16)), (n) => n.toString(16).padStart(2, "0")).join(""); }
export function addressKey(value?: NetworkAddress) { return value ? `${value.interface_id}|${value.address}` : ""; }
export function parseAddressKey(key: string): NetworkAddress | undefined {
  if (!key) return undefined;
  const [interface_id, address] = key.split("|");
  return { interface_id, address };
}
export function networkAddresses(inventory?: NetworkView, family?: "ipv4" | "ipv6", interfaceID?: string) {
  return (inventory?.interfaces ?? []).filter((item) => item.present && (!interfaceID || item.interface.id === interfaceID)).flatMap(({ interface: n }) =>
    (n.usable_addresses ?? []).map((cidr) => cidr.split("/")[0]).filter((ip) => !family || ip.includes(":") === (family === "ipv6")).map((ip) => ({ value: addressKey({ interface_id: n.id, address: ip }), label: `${n.name} · ${ip}` })));
}
export function interfaceName(inventory: NetworkView | undefined, id?: string) {
  if (!id) return "按宿主机路由";
  const item = inventory?.interfaces.find((n) => n.interface.id === id);
  return item ? `${item.interface.name}${item.present ? "" : "（已消失）"}` : `未找到网卡（${id.slice(0, 8)}）`;
}
