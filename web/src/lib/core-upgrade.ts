export type CoreServerStatus = "upgrade" | "pending" | "offline" | "error" | "ready";
export type CoreServer = { id: number; name: string; version: string; status: CoreServerStatus; message: string };
export type CoreServerFilter = CoreServerStatus | "all" | "attention";

const priority: Record<CoreServerStatus, number> = { error: 0, offline: 1, pending: 2, upgrade: 3, ready: 4 };

export function coreServerPage(servers: CoreServer[], query: string, filter: CoreServerFilter, requestedPage: number, pageSize = 8) {
  const needle = query.trim().toLocaleLowerCase();
  const matched = servers.filter(s => (filter === "all" || (filter === "attention" ? s.status !== "ready" : s.status === filter)) && (!needle || `${s.name} ${s.id} ${s.version}`.toLocaleLowerCase().includes(needle)))
    .sort((a, b) => priority[a.status] - priority[b.status] || a.name.localeCompare(b.name) || a.id - b.id);
  const pages = Math.max(1, Math.ceil(matched.length / pageSize));
  const page = Math.max(1, Math.min(pages, Number.isFinite(requestedPage) ? Math.trunc(requestedPage) : 1));
  return { total: matched.length, pages, page, rows: matched.slice((page - 1) * pageSize, page * pageSize) };
}
