import test from "node:test";
import assert from "node:assert/strict";
import { QueryClient } from "@tanstack/react-query";
import { loadSession, retrySession } from "../src/lib/session.ts";

const httpError = status => Object.assign(new Error("fixture"), { status });

test("only an explicit 401 marks a session unauthenticated", async () => {
  assert.equal(await loadSession(async () => { throw httpError(401); }), null);
  for (const error of [httpError(502), httpError(503), httpError(429), new TypeError("network offline")]) {
    await assert.rejects(loadSession(async () => { throw error; }), e => e === error);
  }
});

test("a transient background failure retains the signed-in user", async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const key = ["auth", "me"];
  const user = { id: 1, role: "admin" };
  client.setQueryData(key, user);
  await assert.rejects(client.fetchQuery({ queryKey: key, queryFn: () => loadSession(async () => { throw httpError(503); }) }));
  assert.deepEqual(client.getQueryData(key), user);
  await client.fetchQuery({ queryKey: key, queryFn: () => loadSession(async () => { throw httpError(401); }) });
  assert.equal(client.getQueryData(key), null);
  client.clear();
});

test("a failed initial check remains unknown instead of becoming logged out", async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const key = ["auth", "me"];
  await assert.rejects(client.fetchQuery({ queryKey: key, queryFn: () => loadSession(async () => { throw new TypeError("offline"); }) }));
  assert.equal(client.getQueryData(key), undefined);
  assert.equal(client.getQueryState(key).status, "error");
  client.clear();
});

test("transient session retries are bounded", () => {
  assert.equal(retrySession(0, httpError(503)), true);
  assert.equal(retrySession(2, httpError(503)), false);
  assert.equal(retrySession(0, httpError(401)), false);
});
