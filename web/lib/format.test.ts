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

test("shortId keeps the random end of a UUIDv7, not its timestamp", () => {
  assert.equal(shortId("01a188e5-22f9-7bb2-99b5-8e08aaa94bdd"), "aaa94bdd");
  assert.notEqual(shortId("01a188e5-22f9-7bb2-99b5-8e08aaa94bdd"), shortId("01a188e5-265a-712d-a410-ff6cbf5b6b48"));
});
