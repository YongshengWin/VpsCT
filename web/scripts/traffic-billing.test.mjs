import test from "node:test";
import assert from "node:assert/strict";
import { BILLING_OPTIONS, GiB, billingMode, billingLabel, quotaBilled, trafficIO, trafficUsage, validGiB, gbToBytes, bytesToGb, validCalibration, calibrationPayload, trafficDayLabel, trafficPeriodDate } from "../src/lib/traffic-billing.ts";
import { SERVER_BILLING_OPTIONS, serverBillingMode, serverBillingLabel } from "../src/lib/server-billing.ts";

test("server and share options share one definition; legacy mode remains dual", () => {
  assert.equal(SERVER_BILLING_OPTIONS, BILLING_OPTIONS);
  assert.deepEqual(BILLING_OPTIONS.map((o) => o.value), ["dual", "up", "down", "max"]);
  for (const value of [undefined, null, "", "legacy", "dual", "up", "down", "max"]) {
    assert.equal(serverBillingMode(value), billingMode(value));
    assert.equal(serverBillingLabel(value), billingLabel(value));
  }
  assert.equal(billingMode(undefined), "dual");
  assert.equal(quotaBilled(undefined, 40, 60), 100);
});

test("all four modes use original directions without multiplying raw traffic", () => {
  assert.deepEqual(trafficIO(100, 40), { inbound: 100, outbound: 40, total: 140 });
  assert.equal(quotaBilled("dual", 100, 40), 140);
  assert.equal(quotaBilled("up", 100, 40), 100);
  assert.equal(quotaBilled("down", 100, 40), 40);
  assert.equal(quotaBilled("max", 100, 40), 100);
});

test("max compares period direction totals after a direction reversal", () => {
  const samples = [[100, 0], [0, 80], [0, 60]];
  const inbound = samples.reduce((sum, [up]) => sum + up, 0);
  const outbound = samples.reduce((sum, [, down]) => sum + down, 0);
  assert.equal(quotaBilled("max", inbound, outbound), 140);
  assert.equal(trafficIO(inbound, outbound).total, 240);
  assert.notEqual(quotaBilled("max", inbound, outbound), samples.reduce((sum, [up, down]) => sum + Math.max(up, down), 0));
});

test("calibrated API billed amount wins; raw totals and quota progress remain separate", () => {
  const usage = trafficUsage({ inbound: 100, outbound: 200, billed: 1500, used: 1700, billing_mode: "max", quota: 2000 }, "dual");
  assert.equal(usage.mode, "max");
  assert.equal(usage.total, 300);
  assert.equal(usage.billed, 1500);
  assert.equal(usage.percent, 75);
  assert.equal(usage.remaining, 500);
  assert.equal(trafficUsage({ billed: 0, used: 50, up: 10, down: 20 }, "dual").billed, 0);
});

test("legacy shares use their own raw fields and used amount, never server traffic", () => {
  const usage = trafficUsage({ used: 60, quota: 100 }, "down", { inbound: 40, outbound: 60 });
  assert.equal(usage.total, 100);
  assert.equal(usage.billed, 60);
  assert.equal(usage.percent, 60);
  assert.equal(usage.remaining, 40);
  assert.equal(trafficUsage({ up: 120, down: 20 }, "max").billed, 120);
  assert.equal(trafficUsage({ inbound: 0, outbound: 0, up: 200, down: 300 }, "dual").total, 0);
  assert.equal(trafficUsage({ used: 120, quota: 100 }).remaining, 0);
  assert.equal(trafficUsage({ used: 120, quota: 0 }).remaining, null);
});

test("accurate directional calibration is not mislabeled as scalar max estimation", () => {
  assert.equal(trafficUsage({ quota_calibrated_at: "2026-10-01T00:00:00Z" }, "max").estimated, true);
  assert.equal(trafficUsage({ quota_calibrated_at: "2026-10-01T00:00:00Z", quota_calibrated_inbound_bytes: 0, quota_calibrated_outbound_bytes: 0 }, "max").estimated, false);
  assert.equal(trafficUsage({ billed_estimated: false, quota_calibrated_at: "2026-10-01T00:00:00Z" }, "max").estimated, false);
  assert.equal(trafficUsage({ billed_estimated: true }, "max").estimated, true);
});

test("GiB editing preserves exact quota bytes, including a single byte", () => {
  for (const value of [1, 12_345, GiB + 1, 112 * GiB + 123_456_789, Number.MAX_SAFE_INTEGER]) {
    assert.equal(gbToBytes(bytesToGb(value)), value);
  }
  assert.equal(gbToBytes("1"), 1_073_741_824);
  assert.equal(bytesToGb(0), "");
  assert.equal(validGiB("0"), true);
  assert.equal(validGiB(""), false);
  assert.equal(validGiB("", true), true);
  for (const invalid of ["-1", "Infinity", "invalid", "8388608"]) assert.equal(validGiB(invalid), false);
});

test("directional calibration sends a complete pair and preserves explicit zero", () => {
  const draft = { kind: "directions", total: "999", inbound: "0", outbound: "1.5" };
  assert.equal(validCalibration(draft), true);
  assert.deepEqual(calibrationPayload(draft), { quota_calibrated_inbound_bytes: 0, quota_calibrated_outbound_bytes: 1.5 * GiB });
  for (const incomplete of [{ ...draft, inbound: "" }, { ...draft, outbound: "" }, { ...draft, inbound: "-1" }]) {
    assert.equal(validCalibration(incomplete, true), false);
    assert.throws(() => calibrationPayload(incomplete, true));
  }
});

test("ordinary edits omit calibration; explicit scalar zero resets calibration", () => {
  const blank = { kind: "total", total: "", inbound: "", outbound: "" };
  assert.deepEqual(calibrationPayload(blank, true), {});
  assert.deepEqual(calibrationPayload({ ...blank, kind: "directions" }, true), {});
  assert.deepEqual(calibrationPayload({ ...blank, total: "0" }), { quota_calibrated_usage_bytes: 0 });
  assert.throws(() => calibrationPayload(blank));
});

test("UTC billing dates do not shift to the previous day in western timezones", () => {
  const original = process.env.TZ;
  process.env.TZ = "America/Los_Angeles";
  try {
    assert.equal(trafficDayLabel("2026-06-01T00:00:00Z"), "6/1");
    assert.equal(trafficPeriodDate("2026-06-01T00:00:00Z"), "2026-06-01");
  } finally {
    if (original === undefined) delete process.env.TZ;
    else process.env.TZ = original;
  }
});
