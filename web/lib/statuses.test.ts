import assert from "node:assert/strict";
import { test } from "node:test";

import { isFinal, isStatus } from "./statuses.ts";

test("every status but PROCESSING is final", () => {
  assert.equal(isFinal("APPROVED"), true);
  assert.equal(isFinal("DECLINED"), true);
  assert.equal(isFinal("FAILED"), true);
  assert.equal(isFinal("PROCESSING"), false);
});

test("isStatus accepts the API's statuses and nothing else", () => {
  assert.equal(isStatus("APPROVED"), true);
  assert.equal(isStatus("approved"), false);
  assert.equal(isStatus("PENDING"), false);
  assert.equal(isStatus(undefined), false);
});
