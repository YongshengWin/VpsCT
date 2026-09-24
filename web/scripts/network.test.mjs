import test from "node:test";
import assert from "node:assert/strict";
import { canBindNodeNetwork, egressSummary, networkAddresses, networkRequestUncertain, validSOCKS5Credentials } from "../src/lib/network.ts";

test("binding UI checks the public node cipher, not the private server method", () => {
  const node = { core: "singbox", protocol: "ss", params: { cipher: "2022-blake3-aes-128-gcm" } };
  assert.equal(canBindNodeNetwork(node), true);
  assert.equal(canBindNodeNetwork({ ...node, params: { method: node.params.cipher } }), false);
  assert.equal(canBindNodeNetwork({ ...node, params: { cipher: "2022-blake3-aes-256-gcm" } }), false);
  assert.equal(canBindNodeNetwork({ ...node, core: "snell" }), false);
});

test("address choices preserve interface identity and exclude disappeared or unusable addresses", () => {
  const interfaces = [
    { present: false, interface: { id: "old", name: "wan0", usable_addresses: ["192.0.2.10/24"] } },
    { present: true, interface: { id: "new", name: "wan0", addresses: ["192.0.2.10/24", "192.0.2.11/24"], usable_addresses: ["192.0.2.10/24", "2001:db8::1/64"] } },
    { present: true, interface: { id: "legacy", name: "wan1", addresses: ["198.51.100.10/24"] } },
  ];
  assert.deepEqual(networkAddresses({ interfaces }, "ipv4"), [{ value: "new|192.0.2.10", label: "wan0 · 192.0.2.10" }]);
  assert.deepEqual(networkAddresses({ interfaces }, undefined, "old"), []);
  assert.deepEqual(networkAddresses({ interfaces }, "ipv6", "new"), [{ value: "new|2001:db8::1", label: "wan0 · 2001:db8::1" }]);
});

test("network and server errors leave a mutation outcome uncertain", () => {
  assert.equal(networkRequestUncertain(new TypeError("response lost")), true);
  assert.equal(networkRequestUncertain(Object.assign(new Error("gateway"), { status: 502 })), true);
  assert.equal(networkRequestUncertain(Object.assign(new Error("conflict"), { status: 409 })), false);
  assert.equal(networkRequestUncertain(null), false);
});

test("SOCKS credential lengths are bytes and whitespace is meaningful", () => {
  assert.equal(validSOCKS5Credentials({ username: " user ", password: " " }), true);
  assert.equal(validSOCKS5Credentials({ username: "用".repeat(85), password: "a".repeat(255) }), true);
  assert.equal(validSOCKS5Credentials({ username: "用".repeat(86), password: "a" }), false);
  assert.equal(validSOCKS5Credentials({ username: "user", password: "a".repeat(256) }), false);
  assert.equal(validSOCKS5Credentials({ username: "user", password: "" }), false);
  assert.equal(validSOCKS5Credentials({ username: "user", password: "a\u0000b" }), false);
});

test("SOCKS summary uses outer interface and keeps business DNS distinct", () => {
  const outer = { interface_id: "wan", family: "ipv4", dns: { address: "192.0.2.54", port: 53 } };
  const config = { outer, server: "2001:db8::2", server_port: 1080, family: "dual", udp: false, dns: { address: "192.0.2.53", port: 53 } };
  const summary = egressSummary(config, { interfaces: [{ present: true, interface: { id: "wan", name: "wan0" } }] });
  assert.match(summary, /SOCKS5 \[2001:db8::2\]:1080/);
  assert.match(summary, /仅 TCP · 外层 wan0 · 业务 dual · DNS 192.0.2.53:53/);
  assert.doesNotMatch(summary, /192.0.2.54/);
});
