export type BillingMode = "dual" | "up" | "down" | "max";

export const BILLING_OPTIONS: { value: BillingMode; label: string }[] = [
  { value: "dual", label: "双向：入站 + 出站" },
  { value: "up", label: "单向：仅入站" },
  { value: "down", label: "单向：仅出站" },
  { value: "max", label: "单向：本期入站、出站累计取较大值" },
];

export function billingMode(value?: string | null): BillingMode {
  return value === "up" || value === "down" || value === "max" ? value : "dual";
}

export function billingLabel(value?: string | null): string {
  switch (billingMode(value)) {
    case "up": return "仅入站";
    case "down": return "仅出站";
    case "max": return "本期入/出较大值";
    default: return "入站 + 出站";
  }
}

function bytes(value?: number | null): number {
  return value != null && Number.isFinite(value) && value > 0 ? value : 0;
}

/** Raw counters for this subject; never substitute NIC totals for a node. */
export function trafficIO(inbound?: number | null, outbound?: number | null) {
  const input = bytes(inbound);
  const output = bytes(outbound);
  return { inbound: input, outbound: output, total: input + output };
}

/** Inputs must be the accumulated directions for the whole billing period. */
export function quotaBilled(mode: string | null | undefined, inbound: number, outbound: number): number {
  const io = trafficIO(inbound, outbound);
  switch (billingMode(mode)) {
    case "up": return io.inbound;
    case "down": return io.outbound;
    case "max": return Math.max(io.inbound, io.outbound);
    default: return io.total;
  }
}

interface UsageInput {
  inbound?: number;
  outbound?: number;
  up?: number;
  down?: number;
  billed?: number;
  used?: number;
  billing_mode?: string;
  quota?: number;
  billed_estimated?: boolean;
  quota_calibrated_at?: string;
  quota_calibrated_inbound_bytes?: number;
  quota_calibrated_outbound_bytes?: number;
}

interface UsageFallback {
  inbound?: number;
  outbound?: number;
  quota?: number;
}

/** Prefer the API's billed amount, including any operator quota calibration. */
export function trafficUsage(usage?: UsageInput, mode?: string | null, fallback: UsageFallback = {}) {
  const io = trafficIO(usage?.inbound ?? usage?.up ?? fallback.inbound, usage?.outbound ?? usage?.down ?? fallback.outbound);
  const selected = billingMode(usage?.billing_mode ?? mode);
  const billed = bytes(usage?.billed ?? usage?.used ?? quotaBilled(selected, io.inbound, io.outbound));
  const quota = bytes(usage?.quota ?? fallback.quota);
  return {
    ...io,
    mode: selected,
    billed,
    quota,
    percent: quota > 0 ? billed / quota * 100 : 0,
    remaining: quota > 0 ? Math.max(0, quota - billed) : null,
    estimated: usage?.billed_estimated ?? (selected === "max" && !!usage?.quota_calibrated_at && (usage.quota_calibrated_inbound_bytes == null || usage.quota_calibrated_outbound_bytes == null)),
  };
}

export const GiB = 1024 ** 3;

export function validGiB(value: string, allowEmpty = false): boolean {
  if (value.trim() === "") return allowEmpty;
  const amount = Number(value) * GiB;
  return Number.isFinite(amount) && amount >= 0 && Number.isSafeInteger(Math.round(amount));
}

export function gbToBytes(value: number | string): number {
  const amount = Number(value);
  const converted = amount * GiB;
  return Number.isFinite(converted) && converted > 0 && converted <= Number.MAX_SAFE_INTEGER ? Math.round(converted) : 0;
}

/** A saved quota must round-trip without changing bytes during an edit. */
export function bytesToGb(value: number): string {
  return Number.isFinite(value) && value > 0 ? String(value / GiB) : "";
}

export interface QuotaCalibrationDraft {
  kind: "directions" | "total";
  total: string;
  inbound: string;
  outbound: string;
}

export function validCalibration(draft: QuotaCalibrationDraft, optional = false): boolean {
  const fields = draft.kind === "directions" ? [draft.inbound, draft.outbound] : [draft.total];
  return (optional && fields.every((v) => v.trim() === "")) || fields.every((v) => validGiB(v));
}

/** Never send a half pair, or resubmit stale calibration in an ordinary edit. */
export function calibrationPayload(draft: QuotaCalibrationDraft, optional = false): {
  quota_calibrated_usage_bytes?: number;
  quota_calibrated_inbound_bytes?: number;
  quota_calibrated_outbound_bytes?: number;
} {
  if (!validCalibration(draft, optional)) throw new Error("校准量需填写有效的非负 GiB 数值；入站、出站必须同时填写");
  const fields = draft.kind === "directions" ? [draft.inbound, draft.outbound] : [draft.total];
  if (optional && fields.every((v) => v.trim() === "")) return {};
  return draft.kind === "directions"
    ? { quota_calibrated_inbound_bytes: gbToBytes(draft.inbound), quota_calibrated_outbound_bytes: gbToBytes(draft.outbound) }
    : { quota_calibrated_usage_bytes: gbToBytes(draft.total) };
}

export function trafficPeriodDate(value?: string | null): string {
  if (!value) return "-";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "-" : date.toISOString().slice(0, 10);
}

export function trafficDayLabel(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "-" : `${date.getUTCMonth() + 1}/${date.getUTCDate()}`;
}
