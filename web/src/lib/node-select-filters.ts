import type { Node, Server } from "./types";

export interface SelectQuickFilter {
  key: string;
  label: string;
  options: { value: string; label: string; values: string[] }[];
}

export function nodeSelectFilters(nodes: Node[], servers: Server[]): SelectQuickFilter[] {
  const group = (key: string, label: string, value: (n: Node) => string, title: (value: string) => string): SelectQuickFilter => {
    const buckets = new Map<string, string[]>();
    for (const n of nodes) {
      const v = value(n);
      if (!v) continue;
      const values = buckets.get(v);
      if (values) values.push(String(n.id)); else buckets.set(v, [String(n.id)]);
    }
    return { key, label, options: [...buckets].map(([value, values]) => ({ value, label: title(value), values })) };
  };
  const sources: Record<string, string> = { deployed: "受管", imported: "导入", manual: "手动" };
  return [
    group("protocol", "协议", n => n.protocol, v => v.toUpperCase()),
    group("source", "来源", n => n.source, v => sources[v] ?? v),
    group("server", "服务器", n => n.server_id ? String(n.server_id) : "", v => servers.find(s => s.id === Number(v))?.name ?? `服务器 #${v}`),
  ].filter(g => g.options.length > 0);
}
