// The only module that knows the API's HTTP contract. "server-only" makes the
// build fail if a client component imports it, so API_URL never reaches the
// browser.
import "server-only";

import type { Status } from "./statuses.ts";

const API_URL = process.env.API_URL ?? "http://localhost:8080";

export type Payment = {
  transaction_id: string;
  merchant_id: string;
  amount: number;
  currency: string;
  status: Status;
  card_last4: string;
  reason?: string;
  authorization_code?: string;
  created_at: string;
  updated_at: string;
};

export type PaymentEvent = {
  status: Status;
  attempt?: number;
  reason?: string;
  created_at: string;
};

export type ApiError = {
  code: string;
  message: string;
  fields?: { field: string; message: string }[];
};

export type NewPayment = {
  merchant_id: string;
  amount: number;
  currency: string;
  card: { number: string; expiry_month: number; expiry_year: number; cvv: string };
};

// Payments change state while the worker processes them, so nothing is cached.
async function get<T>(path: string): Promise<T | null> {
  const res = await fetch(`${API_URL}${path}`, { cache: "no-store" });
  if (res.status === 404) {
    return null;
  }
  if (!res.ok) {
    throw new Error(`the API answered ${res.status} to GET ${path}`);
  }
  return (await res.json()) as T;
}

export async function listPayments(filter: { merchantId?: string; status?: Status; limit: number }): Promise<Payment[]> {
  const query = new URLSearchParams({ limit: String(filter.limit) });
  if (filter.merchantId) query.set("merchant_id", filter.merchantId);
  if (filter.status) query.set("status", filter.status);
  const body = await get<{ payments: Payment[] }>(`/payments?${query}`);
  return body?.payments ?? [];
}

export function getPayment(id: string): Promise<Payment | null> {
  return get<Payment>(`/payments/${encodeURIComponent(id)}`);
}

export async function listEvents(id: string): Promise<PaymentEvent[]> {
  const body = await get<{ events: PaymentEvent[] }>(`/payments/${encodeURIComponent(id)}/events`);
  return body?.events ?? [];
}

export async function createPayment(
  payment: NewPayment,
  idempotencyKey: string,
): Promise<{ ok: true; payment: Payment } | { ok: false; error: ApiError }> {
  const res = await fetch(`${API_URL}/payments`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(payment),
    cache: "no-store",
  });
  if (res.status === 202) {
    return { ok: true, payment: (await res.json()) as Payment };
  }
  if (res.status >= 400 && res.status < 500) {
    return { ok: false, error: (await res.json()) as ApiError };
  }
  throw new Error(`the API answered ${res.status} to POST /payments`);
}
