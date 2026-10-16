"use server";

import { redirect } from "next/navigation";

import { type ApiError, createPayment } from "@/lib/api";

export type FormValues = { merchant_id: string; amount: string; currency: string; card: string };

// The values go back with every error, so the form can show what was sent.
export type FormState = {
  values?: FormValues;
  rejected?: ApiError;
  unavailable?: boolean;
};

// Runs on the server: the browser posts the form and never sees the API. The
// idempotency key comes from the form, so posting the same form twice gets the
// same payment back instead of a second one.
export async function createTestPayment(_previous: FormState, form: FormData): Promise<FormState> {
  const values: FormValues = {
    merchant_id: String(form.get("merchant_id") ?? ""),
    amount: String(form.get("amount") ?? ""),
    currency: String(form.get("currency") ?? ""),
    card: String(form.get("card") ?? ""),
  };

  const result = await createPayment(
    {
      merchant_id: values.merchant_id,
      amount: Number(values.amount),
      currency: values.currency,
      card: { number: values.card, expiry_month: 12, expiry_year: 2030, cvv: "123" },
    },
    String(form.get("idempotency_key") ?? ""),
  ).catch((err: unknown) => {
    // No answer or a 5xx: the payment may or may not exist. The form keeps its
    // key, so sending it again is safe either way.
    console.error("POST /payments failed:", err);
    return null;
  });

  if (result === null) {
    return { values, unavailable: true };
  }
  if (!result.ok) {
    return { values, rejected: result.error };
  }
  redirect(`/payments/${result.payment.transaction_id}`);
}
