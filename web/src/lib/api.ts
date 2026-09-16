export class ApiError extends Error {
  status: number;
  code: string;
  constructor(status: number, code: string, message: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

let csrfPending: Promise<string> | undefined;
async function csrf(): Promise<string> {
  if (!csrfPending) csrfPending = fetch("/api/v1/auth/csrf", { credentials: "same-origin", cache: "no-store" }).then(async r => r.ok ? (await r.json()).token as string : "").finally(() => { csrfPending = undefined; });
  return csrfPending;
}

export async function api<T = unknown>(path: string, init: RequestInit & { json?: unknown } = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json", ...(init.headers as Record<string, string>) };
  let body = init.body;
  if (init.json !== undefined) {
    headers["Content-Type"] = "application/json";
    body = JSON.stringify(init.json);
  }
  if (!path.startsWith("/api/")) throw new Error("不支持的 API 地址");
  const method = (init.method ?? "GET").toUpperCase();
  if (!["GET", "HEAD", "OPTIONS"].includes(method) && !["/api/v1/auth/login", "/api/v1/auth/login/2fa", "/api/v1/auth/setup"].includes(path)) {
    headers["X-CSRF-Token"] = await csrf();
  }
  const res = await fetch(path, { ...init, headers, body, credentials: "same-origin" });
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  let data: unknown = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    data = text;
  }
  if (!res.ok) {
    const envelope = (data ?? {}) as { error?: { code?: string; message?: string }; code?: string; message?: string };
    const err = envelope.error ?? envelope;
    if (res.status === 401 && !path.startsWith("/api/v1/auth/")) {
      window.dispatchEvent(new CustomEvent("ctlvps:unauthorized"));
    }
    throw new ApiError(res.status, err.code ?? "error", err.message ?? res.statusText);
  }
  return data as T;
}

export const get = <T>(path: string) => api<T>(path);
export const post = <T>(path: string, json?: unknown) => api<T>(path, { method: "POST", json });
export const put = <T>(path: string, json?: unknown) => api<T>(path, { method: "PUT", json });
export const del = <T = void>(path: string) => api<T>(path, { method: "DELETE" });
