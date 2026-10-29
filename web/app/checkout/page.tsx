import type { Metadata } from "next";
import Link from "next/link";

import { getMerchant } from "@/lib/api";

import { CheckoutForm } from "./checkout-form";
import styles from "./page.module.css";

export const metadata: Metadata = { title: "Pago de prueba" };

// Each visit is a new payment: its own idempotency key and order, which a
// static page would give every visitor.
export const dynamic = "force-dynamic";

export default async function CheckoutPage() {
  const merchant = await getMerchant();
  return (
    <>
      <Link href="/" className={styles.back}>
        ← Pagos
      </Link>
      <div className={styles.heading}>
        <h1>Pago de prueba</h1>
        <p className="muted">
          Pagas como cliente de <strong>{merchant?.legal_name ?? "el comercio de prueba"}</strong>. El adquirente
          simulado decide según la tarjeta.
        </p>
      </div>
      <CheckoutForm
        idempotencyKey={crypto.randomUUID()}
        orderId={`orden-${crypto.randomUUID().slice(0, 8)}`}
        acquirer={{
          // The card fields are the acquirer's page, in an iframe: neither
          // this server nor this page's code ever sees the card.
          url: process.env.ACQUIRER_PUBLIC_URL ?? "http://localhost:8090",
          publishableKey: process.env.ACQUIRER_PUBLISHABLE_KEY ?? "",
        }}
      />
    </>
  );
}
