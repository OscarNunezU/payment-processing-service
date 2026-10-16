import type { Metadata } from "next";
import Link from "next/link";

import styles from "./page.module.css";
import { PaymentForm } from "./payment-form";

export const metadata: Metadata = { title: "Pago de prueba" };

// Each visit needs its own idempotency key: a static page would give every
// visitor the same one.
export const dynamic = "force-dynamic";

export default function NewPaymentPage() {
  return (
    <>
      <Link href="/" className={styles.back}>
        ← Pagos
      </Link>
      <div className={styles.heading}>
        <h1>Pago de prueba</h1>
        <p className="muted">El adquirente simulado decide según la tarjeta; después verás cómo se procesa.</p>
      </div>
      <PaymentForm idempotencyKey={crypto.randomUUID()} />
    </>
  );
}
