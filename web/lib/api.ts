// The only module that knows the API's HTTP contract. "server-only" makes the
// build fail if a client component imports it, so the merchant's secret key
// never reaches the browser.
import "server-only";

import type { Status } from "./statuses.ts";

const API_URL = process.env.API_URL ?? "http://localhost:8080";
// The demo merchant's secret key: the app plays that merchant's server.
const API_KEY = process.env.MERCHANT_API_KEY ?? "";

export type HistoryEntry = {
  kind: "status" | "acquirer_call" | "reversal";
  status?: Status;
  attempt?: number;
  outcome?: string;
  latency_ms?: number;
  response_code?: string;
  reason?: string;
  at: string;
};

export type Payment = {
  id: string;
  order_id: string;
  status: Status;
  amount: number;
  currency: string;
  card?: { brand: string; last4: string };
  reason?: string;
  authorization_code?: string;
  decline_code?: string;
  created_at: string;
  updated_at: string;
  history?: HistoryEntry[];
};

export type Merchant = { id: string; legal_name: string; rut: string; status: string };

export type ApiError = {
  code: string;
  message: string;
  fields?: { field: string; message: string }[];
};

export type NewPayment = { order_id: string; amount: number; currency: string; card_token: string };

// What a POST answered: the payment, final (201) or still processing (202),
// or why it was refused. retryAfter comes with a 503.
export type Created =
  | { ok: true; httpStatus: 201 | 202; replayed: boolean; payment: Payment }
  | { ok: false; httpStatus: number; error: ApiError; retryAfter?: number };

function headers(extra: Record<string, string> = {}): Record<string, string> {
  return { Authorization: `Bearer ${API_KEY}`, ...extra };
}

// Payments change state in the background, so nothing is cached.
async function get<T>(path: string): Promise<T | null> {
  const res = await fetch(`${API_URL}${path}`, { headers: headers(), cache: "no-store" });
  if (res.status === 404) {
    return null;
  }
  if (!res.ok) {
    throw new Error(`the API answered ${res.status} to GET ${path}`);
  }
  return (await res.json()) as T;
}

export async function listPayments(filter: {
  status?: Status;
  orderId?: string;
  startingAfter?: string;
  limit: number;
}): Promise<{ data: Payment[]; has_more: boolean }> {
  const query = new URLSearchParams({ limit: String(filter.limit) });
  if (filter.status) query.set("status", filter.status);
  if (filter.orderId) query.set("order_id", filter.orderId);
  if (filter.startingAfter) query.set("starting_after", filter.startingAfter);
  return (await get<{ data: Payment[]; has_more: boolean }>(`/v1/payments?${query}`)) ?? { data: [], has_more: false };
}

export function getPayment(id: string): Promise<Payment | null> {
  return get<Payment>(`/v1/payments/${encodeURIComponent(id)}`);
}

export function getMerchant(): Promise<Merchant | null> {
  return get<Merchant>("/v1/merchant");
}

// createPayment throws only when there is no answer, or a 5xx other than
// 503: then the payment may or may not exist, and sending the same request
// again, with the same key, is the way to find out.
export async function createPayment(payment: NewPayment, idempotencyKey: string): Promise<Created> {
  const res = await fetch(`${API_URL}/v1/payments`, {
    method: "POST",
    headers: headers({ "Content-Type": "application/json", "Idempotency-Key": idempotencyKey }),
    body: JSON.stringify(payment),
    cache: "no-store",
  });
  if (res.status === 201 || res.status === 202) {
    return {
      ok: true,
      httpStatus: res.status,
      replayed: res.headers.get("Idempotent-Replayed") === "true",
      payment: (await res.json()) as Payment,
    };
  }
  if ((res.status >= 400 && res.status < 500) || res.status === 503) {
    const body = (await res.json()) as { error: ApiError };
    const retryAfter = Number(res.headers.get("Retry-After")) || undefined;
    return { ok: false, httpStatus: res.status, error: body.error, retryAfter };
  }
  throw new Error(`the API answered ${res.status} to POST /v1/payments`);
}
