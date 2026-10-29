import assert from "node:assert/strict";
import { test } from "node:test";

import { refusal } from "./refusal.ts";

test("a 4xx and an acquirer refusal create nothing", () => {
  assert.equal(refusal(422, "amount_exceeds_max"), "not_created");
  assert.equal(refusal(409, "order_has_active_payment"), "not_created");
  assert.equal(refusal(503, "acquirer_unavailable"), "not_created");
});

test("any other 5xx leaves the payment unknown", () => {
  assert.equal(refusal(503, "unavailable"), "uncertain");
  assert.equal(refusal(500, "internal"), "uncertain");
});
