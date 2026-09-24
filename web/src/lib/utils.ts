import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

export function displayName(u?: { display_name?: string; nickname?: string; username?: string } | null): string {
  const n = (u?.display_name || u?.nickname || u?.username || "").trim();
  return n || "—";
}

export function fmtBytes(n: number | undefined | null, digits = 1): string {
  if (n === undefined || n === null || Number.isNaN(n)) return "-";
  if (n === 0) return "0 B";
  const units = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
  let i = 0;
  let v = Math.abs(n);
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${n < 0 ? "-" : ""}${v.toFixed(i === 0 ? 0 : digits)} ${units[i]}`;
}

export function fmtRate(bps: number | undefined): string {
  if (!bps) return "0 B/s";
  return fmtBytes(bps) + "/s";
}

export function fmtDate(s?: string | null, withTime = true): string {
  if (!s) return "-";
  const d = new Date(s);
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 1971) return "-";
  const pad = (n: number) => String(n).padStart(2, "0");
  const date = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
  return withTime ? `${date} ${pad(d.getHours())}:${pad(d.getMinutes())}` : date;
}

export function fmtAgo(s?: string | null): string {
  if (!s) return "从未";
  const t = new Date(s).getTime();
  if (Number.isNaN(t)) return "-";
  const diff = Math.max(0, Date.now() - t) / 1000;
  if (diff < 60) return `${Math.floor(diff)} 秒前`;
  if (diff < 3600) return `${Math.floor(diff / 60)} 分钟前`;
  if (diff < 86400) return `${Math.floor(diff / 3600)} 小时前`;
  return `${Math.floor(diff / 86400)} 天前`;
}

export function fmtDuration(sec: number): string {
  if (!sec) return "-";
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (d > 0) return `${d} 天 ${h} 小时`;
  if (h > 0) return `${h} 小时 ${m} 分`;
  return `${m} 分`;
}

export const GiB = 1024 ** 3;

export function gbToBytes(gb: number | string): number {
  const v = Number(gb);
  return Number.isFinite(v) && v > 0 ? Math.round(v * GiB) : 0;
}

export function bytesToGb(b: number): string {
  return b > 0 ? String(Math.round((b / GiB) * 100) / 100) : "";
}

export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand("copy");
    document.body.removeChild(ta);
    return ok;
  }
}

export const PROTOCOL_LABELS: Record<string, string> = {
  vless: "VLESS Reality",
  anytls: "AnyTLS",
  hysteria2: "Hysteria2",
  tuic: "TUIC v5",
  trojan: "Trojan",
  ss: "Shadowsocks 2022",
  snell: "Snell",
  vmess: "VMess",
  ssh: "SSH（TCP）",
  wireguard: "WireGuard",
  mieru: "mieru",
  socks5: "SOCKS5",
  http: "HTTP",
};

const PROTOCOL_NAME: Record<string, string> = {
  vless: "VLESS",
  anytls: "AnyTLS",
  hysteria2: "Hysteria2",
  tuic: "TUIC",
  trojan: "Trojan",
  ss: "SS",
  snell: "Snell",
  vmess: "VMess",
  ssh: "SSH（TCP）",
  wireguard: "WireGuard",
  mieru: "mieru",
  socks5: "SOCKS5",
  http: "HTTP",
};

/** Default deployed node name: DMIT-US-VLESS */
export function defaultNodeName(server: { name?: string; region?: string }, protocol: string): string {
  const region = (server.region ?? "").trim().toUpperCase();
  const parts: string[] = [];
  if (server.name?.trim()) parts.push(server.name.trim());
  if (region) parts.push(region);
  const proto = PROTOCOL_NAME[protocol] ?? protocol;
  if (proto) parts.push(proto);
  return parts.join("-");
}

export function trafficTotal(up = 0, down = 0): number {
  return up + down;
}

/** 1–28 keep that day; 29–31 → 31 (last day of month); empty / invalid → 0. */
export function parseResetDay(raw: string | number): number {
  const n = typeof raw === "number" ? raw : raw.trim() === "" ? 0 : Number(raw);
  if (!Number.isFinite(n)) return 0;
  const d = Math.trunc(n);
  if (d <= 0) return 0;
  if (d >= 29) return 31;
  return d;
}

export function formatResetDay(n: number): string {
  if (n <= 0) return "";
  if (n >= 29) return "每月最后一天";
  return `每月${n}日`;
}

export const FORMAT_LABELS: Record<string, string> = {
  auto: "自动识别",
  mihomo: "Clash / mihomo",
  surge: "Surge",
  shadowrocket: "Shadowrocket",
  singbox: "sing-box",
  raw: "Base64 (v2rayN / v2rayU)",
  uri: "链接列表",
};

export const STATUS_LABELS: Record<string, string> = {
  active: "正常",
  exhausted: "流量用尽",
  expired: "已过期",
  paused: "已暂停",
  revoked: "已撤销",
  online: "在线",
  offline: "离线",
  pending: "待接入",
};
