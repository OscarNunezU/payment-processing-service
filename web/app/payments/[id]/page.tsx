import type { Metadata } from "next";
import Link from "next/link";
import { notFound } from "next/navigation";

import { StatusBadge } from "@/components/status-badge";
import { getPayment, listEvents } from "@/lib/api";
import { formatAmount, formatDateTime, formatTime, shortId } from "@/lib/format";
import { isFinal } from "@/lib/statuses";

import { AutoRefresh } from "./auto-refresh";
import styles from "./page.module.css";

// Since Next 15, params is a promise.
type Props = { params: Promise<{ id: string }> };

export async function generateMetadata({ params }: Props): Promise<Metadata> {
  const { id } = await params;
  return { title: `Pago ${shortId(id)}` };
}

export default async function PaymentPage({ params }: Props) {
  const { id } = await params;
  const [payment, events] = await Promise.all([getPayment(id), listEvents(id)]);
  if (!payment) notFound();

  return (
    <>
      <Link href="/" className={styles.back}>
        ← Pagos
      </Link>

      <div className={styles.layout}>
        <section className={`card ${styles.summary}`}>
          <StatusBadge status={payment.status} />
          <p className={styles.amount}>
            {formatAmount(payment.amount, payment.currency)} <span className="muted">{payment.currency}</span>
          </p>
          <dl className={styles.facts}>
            <dt>Pago</dt>
            <dd className="mono">{payment.transaction_id}</dd>
            <dt>Comercio</dt>
            <dd>{payment.merchant_id}</dd>
            <dt>Tarjeta</dt>
            <dd>•••• {payment.card_last4}</dd>
            {payment.authorization_code && (
              <>
                <dt>Autorización</dt>
                <dd className="mono">{payment.authorization_code}</dd>
              </>
            )}
            {payment.reason && (
              <>
                <dt>Motivo</dt>
                <dd className="mono">{payment.reason}</dd>
              </>
            )}
            <dt>Creado</dt>
            <dd>{formatDateTime(payment.created_at)}</dd>
          </dl>
          <AutoRefresh active={!isFinal(payment.status)} />
        </section>

        <section className={`card ${styles.history}`}>
          <h2 className={styles.historyTitle}>Historia</h2>
          {events.length === 0 ? (
            <p className="muted">Este pago es anterior a la historia: no tiene eventos.</p>
          ) : (
            <ol className={styles.timeline}>
              {events.map((e, i) => (
                <li key={i} className={styles.event}>
                  <span className={`${styles.dot} ${styles[e.status]}`} aria-hidden="true" />
                  <time className={`mono ${styles.time}`} dateTime={e.created_at}>
                    {formatTime(e.created_at)}
                  </time>
                  <span className={styles.eventStatus}>{e.status}</span>
                  <span className={styles.chips}>
                    {e.attempt ? <span className={styles.chip}>intento {e.attempt}</span> : null}
                    {e.reason && <span className={styles.chip}>{e.reason}</span>}
                  </span>
                </li>
              ))}
            </ol>
          )}
        </section>
      </div>
    </>
  );
}
