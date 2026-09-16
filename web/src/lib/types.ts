export type Role = "admin" | "user";

export interface User {
  id: number;
  username: string;
  nickname: string;
  display_name: string;
  role: Role;
  enabled: boolean;
  /** "" (initials) | "preset:<id>" (built-in SVG) | "/api/v1/users/<id>/avatar?v=…" (uploaded) */
  avatar: string;
  totp_enabled: boolean;
  created_at: string;
  last_login_at?: string | null;
}

export interface Metrics {
  cpu_percent: number;
  load1: number;
  load5: number;
  mem_total: number;
  mem_used: number;
  swap_total: number;
  swap_used: number;
  disk_total: number;
  disk_used: number;
  uptime_sec: number;
  net_rx: number;
  net_tx: number;
  net_rx_rate: number;
  net_tx_rate: number;
  tcp_conns: number;
  udp_conns: number;
  processes: number;
  hostname: string;
  kernel: string;
  arch: string;
  interface: string;
}

export interface CoreStatus {
  name: string;
  version: string;
  installed: boolean;
  active: boolean;
  wanted: boolean;
  rss_bytes: number;
  cpu_percent: number;
  nrestarts: number;
  since?: string;
  last_error?: string;
  instances?: number;
}

export interface Diagnostics {
 security_version?: number;
 security_policy?: boolean;
 security_paused?: boolean;
 metering_error?: string;
  maintenance?: number;
  cores: CoreStatus[] | null;
  certs?: { domain: string; mode: string; not_after: string; issuer: string }[];
  clock_skew_ms: number;
  bbr: boolean;
  congestion_ctl: string;
  ipv6_reachable: boolean;
  ipv4_reachable: boolean;
  oom_events: number;
  nftables: boolean;
  systemd: boolean;
  time_sync: boolean;
  warnings?: string[];
  recent_errors?: string[];
  connlog_lag: number;
  binary_sha256?: string;
}

export interface AgentUpdateInfo {
  supported: boolean;
  outdated: boolean;
  current_sha?: string;
  latest_sha?: string;
  arch?: string;
  command?: string;
}

export interface Agent {
  id: number;
  server_id: number;
  version: string;
  last_seen_at?: string | null;
  public_ipv4: string;
  public_ipv6: string;
  applied_revision: number;
  apply_error: string;
}

export interface ServerUsage {
  server_id: number;
  period_start: string;
  up: number;
  down: number;
  inbound?: number;
  outbound?: number;
  total?: number;
  billed: number;
  one_way: number;
  two_way: number;
  quota: number;
  percent: number;
  over_quota: boolean;
}

export interface Server {
  id: number;
  name: string;
  region: string;
  public_host: string;
  tags: string[];
  notes: string;
  quota_bytes: number;
  quota_reset_day: number;
  quota_billing: string;
  core_mode: "stable" | "lean";
  ipv4_only: boolean;
  cert_mode: string;
  enabled: boolean;
  created_at: string;
  agent?: Agent;
  agent_status: "pending" | "online" | "offline";
  metrics?: Metrics;
  diagnostics?: Diagnostics;
  usage?: ServerUsage;
  node_count: number;
  desired?: { revision: number; status: string; in_sync: boolean; error?: string };
  agent_update?: AgentUpdateInfo;
}

export interface Node {
 traffic?: { inbound: number; outbound: number; total: number; days: number; has_data: boolean; first_sample?: string; last_sample?: string };
  id: number;
  name: string;
  protocol: string;
  server: string;
  port: number;
  params: Record<string, unknown>;
  source: "manual" | "imported" | "deployed" | "chain";
  server_id?: number | null;
  external_sub_id?: number | null;
  share_id?: number | null;
  listen_port: number;
  core: string;
  enabled: boolean;
  revoked: boolean;
  tags: string[];
  chain_front_node_id?: number | null;
  chain_front_name?: string;
  sort_order: number;
  created_at: string;
  uri?: string;
  server_name?: string;
  share_name?: string;
  external_name?: string;
}

export interface ExternalSubscription {
  id: number;
  name: string;
  url: string;
  user_agent: string;
  sync_interval_min: number;
  enabled: boolean;
  last_sync_at?: string | null;
  last_error: string;
  node_count: number;
  userinfo?: { upload: number; download: number; total: number; expire: number } | null;
  created_at: string;
}

export type GroupType = "select" | "url-test" | "fallback" | "load-balance" | "relay";

export interface ProxyGroup {
  name: string;
  type: GroupType;
  proxies: string[];
  node_ids?: number[];
  include_all?: boolean;
  filter?: string;
  exclude_filter?: string;
  url?: string;
  interval?: number;
  tolerance?: number;
  lazy?: boolean;
  strategy?: string;
  icon?: string;
  hidden?: boolean;
  dialer_proxy?: string;
  disable_udp?: boolean;
}

/** Normalize groups coming from the API/presets: Go may serialize an empty
 *  member list as `null`, and the editor relies on `proxies` being an array. */
export function normalizeGroups(groups?: ProxyGroup[] | null): ProxyGroup[] {
  return (groups ?? []).map((g) => ({ ...g, proxies: Array.isArray(g.proxies) ? g.proxies : [] }));
}

export interface ChainSpec {
  name: string;
  front_node_id: number;
  landing_node_id: number;
}

export interface NodeSelection {
  include_all?: boolean;
  node_ids: number[];
  external_sub_ids: number[];
  tags?: string[];
  filter?: string;
  exclude_filter?: string;
}

export interface Subscription {
  id: number;
  name: string;
  kind: string; // Supported kinds: generated, imported, share; legacy rows may differ.
  supported?: boolean;
  token: string;
  token_hint: string;
  short_code: string;
  template_id?: number | null;
  default_format: string;
  proxy_groups: ProxyGroup[];
  chains: ChainSpec[];
  rules: string[];
  rule_providers?: Record<string, unknown>;
  node_selection: NodeSelection;
  source_external_id?: number | null;
  expire_at?: string | null;
  traffic_limit_bytes: number;
  reset_day: number;
  next_reset?: string | null;
  userinfo_header: boolean;
  show_info_nodes: boolean;
  owner_user_id: number;
  allowed_user_ids: number[];
  share_id?: number | null;
  enabled: boolean;
  access_count: number;
  last_access_at?: string | null;
  created_at: string;
  updated_at: string;
  links: Record<string, string>;
  short_link?: string;
  node_count: number;
  share_name?: string;
}

export interface RuleTemplate {
  id: number;
  name: string;
  kind: "mihomo" | "surge" | "singbox" | "shadowrocket";
  description: string;
  content: string;
  variables?: Record<string, unknown>;
  is_builtin: boolean;
}

export interface Preset {
  id: number;
  name: string;
  groups: ProxyGroup[];
  rules: string[];
  is_builtin: boolean;
}

export interface ShareTarget {
  server_id: number;
  protocols: string[];
}

export type ShareStatus = "active" | "exhausted" | "expired" | "paused" | "revoked";

export interface Share {
  id: number;
  name: string;
  user_id?: number | null;
  targets: ShareTarget[];
  extra_node_ids: number[];
  quota_bytes: number;
  billing_mode: string;
  reset_day: number;
  used_upload: number;
  used_download: number;
  period_start: string;
  expires_at?: string | null;
  status: ShareStatus;
  template_id?: number | null;
  connlog_enabled: boolean;
  notes: string;
  subscription_id?: number | null;
  created_at: string;
  usage: { used: number; inbound?: number; outbound?: number; total?: number; one_way: number; two_way: number; quota: number; percent: number; next_reset?: string; remaining: number };
  subscription?: Subscription;
  nodes?: Node[];
  user_name?: string;
}

export interface TrafficPoint {
  bucket: string;
  up: number;
  down: number;
}

export interface Series {
 has_data: boolean;
 total: number;
  subject: string;
  id: number;
  from: string;
  to: string;
  points: TrafficPoint[];
  total_up: number;
  total_down: number;
}

export interface GeoInfo {
  country?: string;
  province?: string;
  city?: string;
  district?: string;
  isp?: string;
  label: string;
}

export interface ConnEvent {
  id: number;
  server_id: number;
  node_id: number;
  share_id?: number | null;
  ts: string;
  network: string;
  dest_host: string;
  dest_port: number;
  src_host?: string;
  src_geo?: GeoInfo;
}

export interface ConnClientHit {
  key: string;
  hits: number;
  geo?: GeoInfo;
}

export interface AuditEvent {
  id: number;
  ts: string;
  user_id?: number | null;
  username: string;
  action: string;
  target: string;
  detail?: unknown;
  ip: string;
}

export interface BanRule {
  id: number;
  kind: "ip" | "cidr" | "ua";
  value: string;
  reason: string;
  enabled: boolean;
  expires_at?: string | null;
  created_at: string;
}

export interface AccessLog {
  id: number;
  subscription_id: number;
  ts: string;
  ip: string;
  user_agent: string;
  format: string;
  status: number;
}

export interface Meta {
  version: string;
  site_name: string;
  protocols: string[];
  formats: string[];
  base_url: string;
  connlog: boolean;
}
