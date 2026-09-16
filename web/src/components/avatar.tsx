import * as React from "react";
import { cn, displayName } from "@/lib/utils";

/**
 * Built-in avatars: small geometric SVG illustrations. The id is what gets
 * stored on the user ("preset:<id>"), so ids must stay stable.
 */
export interface AvatarPreset {
  id: string;
  label: string;
  bg: string;
  fg: string;
  draw: (fg: string, bg: string) => React.ReactNode;
}

export const AVATAR_PRESETS: AvatarPreset[] = [
  { id: "sunset", label: "日落", bg: "#FDE7D6", fg: "#F26E21", draw: (fg) => <><circle cx="32" cy="30" r="14" fill={fg} /><rect x="0" y="40" width="64" height="24" fill={fg} opacity="0.35" /></> },
  { id: "mint-wave", label: "海浪", bg: "#D9F5E8", fg: "#10B981", draw: (fg) => <path d="M0 36 C10 26 18 26 28 36 S46 46 64 34 V64 H0 Z" fill={fg} /> },
  { id: "sky-dots", label: "圆点", bg: "#DCEEFF", fg: "#3B82F6", draw: (fg) => <><circle cx="20" cy="20" r="7" fill={fg} /><circle cx="32" cy="32" r="7" fill={fg} opacity="0.75" /><circle cx="44" cy="44" r="7" fill={fg} opacity="0.5" /></> },
  { id: "lavender-ring", label: "圆环", bg: "#E9E3FF", fg: "#7C3AED", draw: (fg) => <circle cx="32" cy="32" r="15" fill="none" stroke={fg} strokeWidth="8" /> },
  { id: "rose-diamond", label: "菱形", bg: "#FFE1EA", fg: "#E11D48", draw: (fg) => <rect x="19" y="19" width="26" height="26" rx="4" transform="rotate(45 32 32)" fill={fg} /> },
  { id: "sand-peak", label: "山峰", bg: "#F5EBD8", fg: "#B45309", draw: (fg) => <><path d="M8 50 L28 20 L40 38 L46 30 L58 50 Z" fill={fg} /><circle cx="48" cy="18" r="5" fill={fg} opacity="0.5" /></> },
  { id: "slate-bolt", label: "闪电", bg: "#E2E8F0", fg: "#334155", draw: (fg) => <path d="M36 10 L20 36 H31 L28 54 L45 27 H34 Z" fill={fg} /> },
  { id: "teal-hex", label: "六边形", bg: "#D5F2F2", fg: "#0D9488", draw: (fg) => <path d="M32 14 L48 23 V41 L32 50 L16 41 V23 Z" fill={fg} /> },
  { id: "indigo-moon", label: "月亮", bg: "#E0E7FF", fg: "#4338CA", draw: (fg, bg) => <><circle cx="32" cy="32" r="16" fill={fg} /><circle cx="39" cy="27" r="13" fill={bg} /></> },
  { id: "lime-leaf", label: "叶子", bg: "#ECFCCB", fg: "#4D7C0F", draw: (fg) => <><path d="M18 46 C18 26 30 16 48 16 C48 36 36 46 18 46 Z" fill={fg} /><path d="M20 44 L44 20" stroke="#ECFCCB" strokeWidth="2.5" strokeLinecap="round" /></> },
  { id: "orange-stripes", label: "条纹", bg: "#FFEBD6", fg: "#EA580C", draw: (fg) => <g stroke={fg} strokeWidth="7" strokeLinecap="round"><path d="M14 44 L44 14" /><path d="M8 30 L30 8" opacity="0.6" /><path d="M32 56 L56 32" opacity="0.6" /></g> },
  { id: "night-star", label: "星星", bg: "#1F2937", fg: "#FBBF24", draw: (fg) => <><path d="M32 12 L36 28 L52 32 L36 36 L32 52 L28 36 L12 32 L28 28 Z" fill={fg} /><circle cx="48" cy="16" r="2" fill={fg} opacity="0.7" /><circle cx="16" cy="48" r="1.5" fill={fg} opacity="0.7" /></> },
];

const presetById = new Map(AVATAR_PRESETS.map((p) => [p.id, p]));

export function PresetAvatar({ preset, size = 32, className }: { preset: AvatarPreset; size?: number; className?: string }) {
  return (
    <svg width={size} height={size} viewBox="0 0 64 64" className={cn("shrink-0 rounded-full", className)} aria-label={preset.label} role="img">
      <rect width="64" height="64" fill={preset.bg} />
      {preset.draw(preset.fg, preset.bg)}
    </svg>
  );
}

// Initial-letter fallback: a stable tint per username.
const INITIAL_TINTS = [
  "bg-tint-peach text-primary",
  "bg-tint-mint text-emerald-700 dark:text-emerald-300",
  "bg-tint-sky text-sky-700 dark:text-sky-300",
  "bg-tint-lavender text-violet-700 dark:text-violet-300",
  "bg-tint-rose text-rose-700 dark:text-rose-300",
  "bg-tint-sand text-amber-800 dark:text-amber-300",
];
function tintFor(name: string) {
  let h = 0;
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) >>> 0;
  return INITIAL_TINTS[h % INITIAL_TINTS.length];
}

export interface AvatarUser {
  username: string;
  nickname?: string;
  display_name?: string;
  avatar?: string | null;
}

/** Avatar renders a user's picture: preset SVG, uploaded image or initial. */
export function Avatar({ user, size = 32, className }: { user: AvatarUser; size?: number; className?: string }) {
  const a = user.avatar ?? "";
  if (a.startsWith("preset:")) {
    const p = presetById.get(a.slice("preset:".length));
    if (p) return <PresetAvatar preset={p} size={size} className={className} />;
  }
  if (a.startsWith("/") || a.startsWith("data:")) {
    return <img src={a} alt="" width={size} height={size} className={cn("shrink-0 rounded-full object-cover", className)} style={{ width: size, height: size }} />;
  }
  const label = displayName(user);
  const ch = (label === "—" ? "?" : label).trim().charAt(0).toUpperCase() || "?";
  return (
    <span className={cn("inline-flex shrink-0 items-center justify-center rounded-full font-bold leading-none", tintFor(label), className)} style={{ width: size, height: size, fontSize: Math.round(size * 0.42) }} aria-hidden>
      {ch}
    </span>
  );
}

/**
 * fileToAvatarDataURL crops an image file to a centred square, downsizes it
 * to `px` and returns a compact JPEG data URL supported by the server.
 */
export async function fileToAvatarDataURL(file: File, px = 256): Promise<string> {
  const url = URL.createObjectURL(file);
  try {
    const img = await new Promise<HTMLImageElement>((res, rej) => {
      const i = new Image();
      i.onload = () => res(i);
      i.onerror = () => rej(new Error("无法读取图片"));
      i.src = url;
    });
    const side = Math.min(img.naturalWidth, img.naturalHeight);
    const sx = (img.naturalWidth - side) / 2;
    const sy = (img.naturalHeight - side) / 2;
    const canvas = document.createElement("canvas");
    canvas.width = px;
    canvas.height = px;
    const ctx = canvas.getContext("2d");
    if (!ctx) throw new Error("浏览器不支持图片处理");
    ctx.drawImage(img, sx, sy, side, side, 0, 0, px, px);
    const keepAlpha = file.type === "image/png" && file.size < 64 * 1024;
    if (keepAlpha) return canvas.toDataURL("image/png");
    return canvas.toDataURL("image/jpeg", 0.86);
  } finally {
    URL.revokeObjectURL(url);
  }
}
