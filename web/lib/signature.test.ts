import assert from "node:assert/strict";
import { test } from "node:test";

import { verify } from "./signature.ts";

// Signed by the webhooks service's domain.Sign, in Go, for this body at this
// time: the receiver must agree with the sender byte for byte.
const SECRET = "whsec_local_only_tienda_uno";
const BODY = `{"id":"01a18855-249b-7d8a-9aec-f70f853b492b","type":"payment.approved"}`;
const SIGNED_AT = 1793278800;
const HEADER = `t=${SIGNED_AT},v1=0740a31a997e966622c7d1caf8fa664e3243b0823bc1c51ba3cef09ab923aa0c`;

test("the signature the service sends verifies", () => {
  assert.equal(verify(SECRET, HEADER, BODY, SIGNED_AT + 60), "valid");
});

test("another body, another secret or a malformed header do not", () => {
  assert.equal(verify(SECRET, HEADER, BODY.replace("approved", "declined"), SIGNED_AT), "invalid");
  assert.equal(verify("whsec_local_only_tienda_dos", HEADER, BODY, SIGNED_AT), "invalid");
  assert.equal(verify(SECRET, "v1=abc", BODY, SIGNED_AT), "invalid");
  assert.equal(verify(SECRET, "", BODY, SIGNED_AT), "invalid");
});

test("a timestamp moved to look recent breaks the signature", () => {
  const moved = HEADER.replace(String(SIGNED_AT), String(SIGNED_AT + 3600));
  assert.equal(verify(SECRET, moved, BODY, SIGNED_AT + 3600), "invalid");
});

test("an old signature is stale", () => {
  assert.equal(verify(SECRET, HEADER, BODY, SIGNED_AT + 5 * 60 + 1), "stale");
});
