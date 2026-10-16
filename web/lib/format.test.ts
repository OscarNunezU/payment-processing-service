import assert from "node:assert/strict";
import { test } from "node:test";

import { formatAmount, formatDateTime, formatTime, shortId } from "./format.ts";

test("amounts are shown in each currency's minor unit", () => {
  assert.equal(formatAmount(15000, "CLP"), "$15.000");
  assert.equal(formatAmount(1250, "USD"), "$12,50");
});

test("times are shown in Chilean time, whatever the server's zone", () => {
  // October is daylight saving time in Chile: UTC-3.
  assert.equal(formatDateTime("2026-10-16T12:41:05Z"), "16-10 09:41:05");
  assert.equal(formatTime("2026-10-16T12:41:03.120Z"), "09:41:03.120");
});

test("shortId keeps the first 8 characters of the UUID", () => {
  assert.equal(shortId("7f3a1c2e-0b9d-4c1e-9b41-5d2f8a6e3c10"), "7f3a1c2e");
});
