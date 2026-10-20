// Local NAT UI fixture. All API data is synthetic; no VPS or real account
// is involved. Build web/dist, run this server, then interact through cua_repl.
import assert from "node:assert/strict";
import fs from "node:fs";
import http from "node:http";
import path from "node:path";

const web = path.resolve(import.meta.dirname, "../web/dist");
assert(fs.existsSync(path.join(web, "index.html")), "build web/dist first");
const original = { schema: 1, mappings: [{ public_port: 30001, local_port: 10001, count: 2, network: "tcp" }] };
const state = {
  online: true, conflict: false, writes: [],
  view: { server_id: 1, revision: 4, desired_config: original, report: { version: 2, source: "controller", configured: true, status: "applied", config: original }, editable: true, pending: false },
};
function summary() {
  const r = state.view.report;
  return { supported: true, source: r.source, configured: r.configured, status: state.view.pending ? "pending" : r.status, editable: state.online && state.view.editable, reason: state.view.reason, pending: state.view.pending, revision: state.view.revision, ...(!state.view.pending && r.error ? { error: r.error } : {}) };
}
function server(id = 1) {
  return { id, name: id === 1 ? "NAT 验收 VPS" : "普通验收 VPS", region: "", public_host: "198.51.100.10", tags: [], notes: "", quota_bytes: 0, quota_reset_day: 1, quota_billing: "sum", core_mode: "stable", ipv4_only: false, cert_mode: "self_signed", enabled: true, created_at: "2026-01-01T00:00:00Z", agent_status: state.online ? "online" : "offline", node_count: 0, ...(id === 1 ? { nat: summary() } : {}), diagnostics: { cores: [], clock_skew_ms: 0, bbr: true, congestion_ctl: "bbr", ipv4_reachable: true, ipv6_reachable: false, oom_events: 0, nftables: true, systemd: true, time_sync: true, connlog_lag: 0 } };
}
const json = (res, body, status = 200) => { res.writeHead(status, { "Content-Type": "application/json" }); res.end(JSON.stringify(body)); };
const handler = async (req, res) => {
  const url = new URL(req.url, "http://127.0.0.1");
  // Local fixture controls only; these routes are never part of the product.
  if (url.pathname === "/__fixture/state") {
    const mode = url.searchParams.get("mode");
    if (mode === "conflict") { state.conflict = true; return json(res, { ok: true }); }
    state.online = mode !== "offline"; state.view.pending = false;
    state.view.editable = !["local", "local-empty", "local-error", "offline"].includes(mode);
    if (mode?.startsWith("local")) {
      state.view.reason = "本机 root NAT 文件优先，面板只读";
      state.view.report = mode === "local-error" ? { version: 2, source: "local", configured: false, status: "error", error: "本机 NAT 配置文件无效" } : { version: 2, source: "local", configured: mode !== "local-empty", status: mode === "local-empty" ? "disabled" : "applied", config: { schema: 1, mappings: mode === "local-empty" ? [] : [{ public_port: 31001, local_port: 11001, count: 1, network: "udp" }] } };
    } else {
      if (mode === "tcp-only") { state.view.desired_config = original; state.view.revision++; }
      state.view.reason = mode === "offline" ? "agent 离线" : undefined;
      state.view.report = { version: 2, source: "controller", configured: true, status: mode === "error" ? "error" : "applied", config: state.view.desired_config, ...(mode === "error" ? { error: "fixture NAT 应用失败" } : {}) };
    }
    return json(res, { ok: true });
  }
  if (url.pathname === "/api/v1/events") { res.writeHead(200, { "Content-Type": "text/event-stream" }); res.write(": fixture\n\n"); return; }
  if (url.pathname === "/api/v1/auth/setup") return json(res, { needs_setup: false, site_name: "VpsCT" });
  if (url.pathname === "/api/v1/auth/me") return json(res, { id: 1, username: "nat-fixture", display_name: "NAT 验收", role: "admin", enabled: true, avatar: "" });
  if (url.pathname === "/api/v1/auth/csrf") return json(res, { token: "fixture-only" });
  if (url.pathname === "/api/v1/meta") return json(res, { version: "fixture", site_name: "VpsCT", protocols: ["vless", "ss", "hysteria2", "tuic", "mieru"], formats: [], base_url: "", connlog: false });
  if (url.pathname === "/api/v1/settings/core-upgrade") return json(res, { id: "fixture", needs_attention: false, servers: [] });
  if (url.pathname === "/api/v1/servers") return json(res, [server(), server(2)]);
  if (url.pathname === "/api/v1/servers/1") return json(res, server());
  if (url.pathname === "/api/v1/servers/1/nat") {
    if (req.method === "PUT") {
      assert.equal(req.headers["x-csrf-token"], "fixture-only");
      const chunks = []; for await (const chunk of req) chunks.push(chunk);
      const body = JSON.parse(Buffer.concat(chunks).toString()); state.writes.push(body);
      if (state.conflict) { state.conflict = false; state.view.revision++; return json(res, { error: { code: "conflict", message: "NAT 配置已变化，请刷新后重试" } }, 409); }
      if (body.expected_revision !== state.view.revision) return json(res, { error: { code: "conflict", message: "NAT 配置已变化，请刷新后重试" } }, 409);
      state.view.desired_config = body.config; state.view.revision++; state.view.pending = true;
    }
    return json(res, state.view);
  }
  if (url.pathname === "/api/v1/servers/1/network") return json(res, { error: { code: "fixture", message: "fixture inventory unavailable" } }, 503);
  if (/\/traffic$/.test(url.pathname)) return json(res, { has_data: false, points: [], total_up: 0, total_down: 0 });
  if (/\/(samples|desired|transits)$/.test(url.pathname) || url.pathname === "/api/v1/nodes") return json(res, []);
  if (/\/maintenance$/.test(url.pathname)) return json(res, { available: false, jobs: [] });
  if (url.pathname.startsWith("/api/")) return json(res, {});
  const relative = url.pathname.startsWith("/assets/") ? url.pathname.slice(1) : "index.html";
  const file = path.resolve(web, relative);
  if (!file.startsWith(web + path.sep) || !fs.existsSync(file)) { res.writeHead(404); res.end(); return; }
  const ext = path.extname(file); res.setHeader("Content-Type", ext === ".js" ? "text/javascript" : ext === ".css" ? "text/css" : "text/html"); fs.createReadStream(file).pipe(res);
};
const app = http.createServer((req, res) => { handler(req, res).catch((e) => { res.destroy(e); }); });
await new Promise((resolve) => app.listen(0, "127.0.0.1", resolve));
console.log(`NAT fixture http://127.0.0.1:${app.address().port}`);
await new Promise((resolve) => { process.once("SIGINT", resolve); process.once("SIGTERM", resolve); });
app.closeAllConnections();
await new Promise((resolve) => app.close(resolve));
