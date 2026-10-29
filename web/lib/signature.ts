import { createHmac, timingSafeEqual } from "node:crypto";

// How old a signed timestamp a receiver accepts, so a webhook captured on the
// way cannot be replayed later (RFC 0001, section 9).
export const TOLERANCE_SECONDS = 5 * 60;

export type Verdict = "valid" | "invalid" | "stale";

// verify checks a Webhook-Signature header, "t=<unix seconds>,v1=<hex>", as a
// merchant's receiver should: v1 must be the HMAC-SHA256 of "t.body" with the
// secret, and t within TOLERANCE_SECONDS of now. The body is the raw one, as
// it arrived: parsing it first would change what was signed.
export function verify(secret: string, header: string, body: string, nowSeconds: number): Verdict {
  const parts = new Map(header.split(",").map((part) => part.split("=", 2) as [string, string]));
  const t = parts.get("t") ?? "";
  const v1 = parts.get("v1") ?? "";
  if (!/^\d+$/.test(t) || !/^[0-9a-f]{64}$/.test(v1)) {
    return "invalid";
  }
  const expected = createHmac("sha256", secret).update(`${t}.${body}`).digest();
  if (!timingSafeEqual(expected, Buffer.from(v1, "hex"))) {
    return "invalid";
  }
  return Math.abs(nowSeconds - Number(t)) > TOLERANCE_SECONDS ? "stale" : "valid";
}
