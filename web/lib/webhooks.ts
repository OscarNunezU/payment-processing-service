// The webhooks this app received, as the merchant's server would keep them.
// They live in memory, so a restart forgets them: this is a demo receiver.
import "server-only";

import { type Verdict, verify } from "./signature.ts";

const KEEP = 100;

// WEBHOOK_SECRETS maps each local merchant to its signing secret:
// "<merchant id>=<secret>,<merchant id>=<secret>".
const SECRETS = new Map(
  (process.env.WEBHOOK_SECRETS ?? "")
    .split(",")
    .filter(Boolean)
    // Split at the first "=" only, so a secret may hold one.
    .map((pair) => [pair.slice(0, pair.indexOf("=")), pair.slice(pair.indexOf("=") + 1)]),
);

export type Received = {
  id: string;
  type: string;
  paymentId: string;
  merchantId: string;
  status: string;
  verdict: Verdict;
  // times counts the deliveries of this event: more than one is a duplicate,
  // which the receiver answers 200 again without processing it twice.
  times: number;
  firstAt: string;
  lastAt: string;
};

type State = { received: Received[]; failing: boolean };

// A module is evaluated once per bundle, and Next bundles route handlers and
// pages apart, so the state lives on globalThis to be shared.
const state: State = ((globalThis as { webhooks?: State }).webhooks ??= { received: [], failing: false });

export function received(): Received[] {
  return state.received;
}

export function failing(): boolean {
  return state.failing;
}

// setFailing makes the endpoint answer 500, as a merchant's server that is
// down: the service retries, with growing waits.
export function setFailing(on: boolean): void {
  state.failing = on;
}

// receive handles one webhook and returns the HTTP status to answer. id is the
// Webhook-Id header, the event's id, which tells a duplicate apart.
export function receive(header: string, id: string, body: string, now: Date): number {
  if (state.failing) {
    return 500;
  }
  let event: { id?: string; type?: string; payment_id?: string; merchant_id?: string; data?: { status?: string } };
  try {
    event = JSON.parse(body);
  } catch {
    return 400;
  }
  // The secret is chosen by the merchant the body names; the signature then
  // proves the body, that merchant included, is the service's.
  const secret = SECRETS.get(event.merchant_id ?? "");
  const verdict: Verdict = secret ? verify(secret, header, body, Math.floor(now.getTime() / 1000)) : "invalid";
  const at = now.toISOString();

  const seen = state.received.find((r) => r.id === id && r.verdict === "valid");
  if (verdict === "valid" && seen) {
    seen.times++;
    seen.lastAt = at;
    return 200;
  }
  state.received.unshift({
    id,
    type: event.type ?? "",
    paymentId: event.payment_id ?? "",
    merchantId: event.merchant_id ?? "",
    status: event.data?.status ?? "",
    verdict,
    times: 1,
    firstAt: at,
    lastAt: at,
  });
  state.received.length = Math.min(state.received.length, KEEP);
  return verdict === "valid" ? 200 : 400;
}
