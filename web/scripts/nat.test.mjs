import test from "node:test";
import assert from "node:assert/strict";
import { hasNAT, natCovers, natConfigForDeploy, natDraft, natDraftConfig, natState } from "../src/lib/nat.ts";

const config = { schema: 1, mappings: [{ public_port: 30001, local_port: 10001, count: 2, network: "tcp" }] };
const summary = { supported: true, source: "controller", configured: true, status: "applied", editable: true, pending: false, revision: 1 };
test("NAT capability alone never marks an ordinary VPS and offline never claims applied", () => {
  assert.equal(hasNAT({ ...summary, source: "none", configured: false }), false);
  assert.equal(hasNAT({ ...summary, source: "local", configured: false, status: "disabled" }), false);
  assert.equal(hasNAT({ ...summary, source: "local", configured: false, status: "error" }), true);
  assert.notEqual(natState("offline", summary).variant, "success");
  assert.notEqual(natState("pending", summary).variant, "success");
  assert.equal(natState("online", summary).label, "本机已应用");
});
test("a pending revision hides an old applied or failed report; matched failure remains visible", () => {
  const view = { server_id: 1, revision: 2, desired_config: config, editable: true, pending: true, report: { version: 2, source: "controller", configured: true, status: "error", error: "old failure" } };
  assert.equal(natState("online", summary, view).label, "等待 agent 应用");
  assert.equal(natState("online", summary, { ...view, pending: false }).label, "应用失败");
});
test("independent summary and detail polling cannot turn a newer pending or error state green", () => {
  const view = { server_id: 1, revision: 1, desired_config: config, editable: true, pending: false, report: { version: 2, source: "controller", configured: true, status: "applied", config } };
  assert.equal(natState("online", { ...summary, revision: 2, pending: true, status: "pending" }, view).label, "等待 agent 应用");
  assert.equal(natState("online", { ...summary, status: "error" }, view).label, "应用失败");
  assert.equal(natState("online", summary, { ...view, report: null }).variant, "secondary");
  assert.equal(natState("online", summary, view).variant, "success");
});
test("root local authority does not fall back to a controller config when empty or invalid", () => {
  assert.equal(natConfigForDeploy({ desired_config: config, report: { source: "local" } }), undefined);
  assert.equal(natConfigForDeploy({ desired_config: config, report: { source: "controller" } }), config);
});
test("UDP protocols and SS require complete mappings on the same public port", () => {
  assert.equal(natCovers(config, "vless", 30002), true);
  assert.equal(natCovers(config, "vless", 30003), false);
  assert.equal(natCovers(config, "hysteria2"), false);
  assert.equal(natCovers(config, "ss"), false);
  assert.equal(natCovers(config, "ss", 30001, "TCP", "tcp"), true);
  assert.equal(natCovers(config, "ss", 30003, "TCP", "tcp"), false);
  const split = { ...config, mappings: [...config.mappings, { public_port: 30002, local_port: 11002, network: "udp" }] };
  assert.equal(natCovers(split, "ss", 30002), true);
  assert.equal(natCovers(split, "ss", 30001), false);
  assert.equal(natCovers(split, "mieru", 30001, "UDP"), false);
});
test("form rejects overlapping ranges, excessive expansion and privileged redirected ports", () => {
  const draft = natDraft(config);
  assert.deepEqual(natDraftConfig(draft), config);
  draft.mappings.push({ public_port: "30002", local_port: "11000", count: "1", network: "udp" });
  assert.doesNotThrow(() => natDraftConfig(draft));
  draft.mappings[1].network = "both";
  assert.throws(() => natDraftConfig(draft), /重叠/);
  assert.throws(() => natDraftConfig(natDraft({ schema: 1, mappings: [{ public_port: 30001, local_port: 22 }] })), /1–1023/);
  assert.doesNotThrow(() => natDraftConfig(natDraft({ schema: 1, mappings: [{ public_port: 22, local_port: 22 }] })));
  assert.throws(() => natDraftConfig(natDraft({ schema: 1, mappings: [{ public_port: 65535, local_port: 10001, count: 2 }] })), /65535/);
  assert.throws(() => natDraftConfig(natDraft({ schema: 1, mappings: [{ public_port: 20000, local_port: 10000, count: 4097 }] })), /4096/);
});
test("advanced fields accept optional IPv4 and reject loopback, malformed addresses and interfaces", () => {
  const draft = natDraft(config);
  draft.local_address = "192.0.2.10";
  draft.interface = "eth0";
  assert.equal(natDraftConfig(draft).local_address, "192.0.2.10");
  for (const address of ["127.0.0.1", "0.0.0.0", "224.0.0.1", "255.255.255.255", "192.00.2.10", "::1"]) assert.throws(() => natDraftConfig({ ...draft, local_address: address }), /IPv4/);
  assert.throws(() => natDraftConfig({ ...draft, interface: "eth0;id" }), /网卡/);
  for (const name of [".", "..", "eth0".repeat(4)]) assert.throws(() => natDraftConfig({ ...draft, interface: name }), /网卡/);
  assert.throws(() => natDraftConfig({ ...draft, mappings: [{ ...draft.mappings[0], count: "1.5" }] }), /正整数/);
});
