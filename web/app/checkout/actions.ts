"use server";

import { redirect } from "next/navigation";

import { type ApiError, createPayment } from "@/lib/api";
import { refusal } from "@/lib/refusal";

export type Values = { amount: string; currency: string };

// Why the payment was not made, when it was not.
export type State = {
  rejected?: ApiError & { httpStatus: number; retryAfter?: number };
  // unconfirmed: no answer, or one that does not say whether the payment
  // exists. Sending the form again, with the same key, finds out.
  unconfirmed?: { retryAfter?: number };
  tokenError?: string;
};

// Runs on the server, which holds the merchant's secret key. The idempotency
// key and the order come from the form, so sending the same form twice gets
// the same payment back instead of a second one.
export async function pay(values: Values, form: FormData): Promise<State> {
  const result = await createPayment(
    {
      order_id: String(form.get("order_id") ?? ""),
      amount: Number(values.amount),
      currency: values.currency,
      card_token: String(form.get("card_token") ?? ""),
    },
    String(form.get("idempotency_key") ?? ""),
  ).catch((err: unknown) => {
    // No answer: the payment may or may not exist. The form keeps its key,
    // so sending it again is safe either way.
    console.error("POST /v1/payments failed:", err);
    return null;
  });

  if (result === null) {
    return { unconfirmed: {} };
  }
  if (!result.ok && refusal(result.httpStatus, result.error.code) === "uncertain") {
    return { unconfirmed: { retryAfter: result.retryAfter } };
  }
  if (!result.ok) {
    return { rejected: { ...result.error, httpStatus: result.httpStatus, retryAfter: result.retryAfter } };
  }
  // 201 shows the result, and 202 a payment that the page follows until it
  // is final.
  redirect(`/payments/${result.payment.id}`);
}
