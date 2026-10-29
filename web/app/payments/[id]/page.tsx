import type { Metadata } from "next";
import Link from "next/link";
import { notFound } from "next/navigation";

import { AutoRefresh } from "@/components/auto-refresh";
import { StatusBadge } from "@/components/status-badge";
import { getPayment, type HistoryEntry } from "@/lib/api";
import { formatAmount, formatDateTime, formatTime, shortId } from "@/lib/format";
import { isFinal } from "@/lib/statuses";

import styles from "./page.module.css";

// Since Next 15, params is a promise.
type Props = { params: Promise<{ id: string }> };

export async function generateMetadata({ params }: Props): Promise<Metadata> {
  const { id } = await params;
  return { title: `Pago ${shortId(id)}` };
}

// What a line of the history says, and the class of its dot.
function describe(e: HistoryEntry): { title: string; dot: string; chips: string[] } {
  const call = [e.latency_ms ? `${e.latency_ms} ms` : "", e.response_code ?? ""].filter(Boolean);
  switch (e.kind) {
    case "status":
      return { title: e.status ?? "", dot: e.status ?? "", chips: e.reason ? [e.reason] : [] };
    case "acquirer_call":
      // An attempt without an outcome was recorded before its call, and its
      // answer never came: a crash, or a call still in flight.
      return { title: `Intento ${e.attempt}`, dot: "call", chips: [e.outcome ?? "sin respuesta", ...call] };
    case "reversal":
      return { title: "Reverso", dot: "reversal", chips: [e.outcome ?? "sin respuesta", ...call] };
  }
}

export default async function PaymentPage({ params }: Props) {
  const { id } = await params;
  const payment = await getPayment(id);
  if (!payment) notFound();
  const history = payment.history ?? [];

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
            <dd className="mono">{payment.id}</dd>
            <dt>Orden</dt>
            <dd className="mono">{payment.order_id}</dd>
            {payment.card && (
              <>
                <dt>Tarjeta</dt>
                <dd>
                  {payment.card.brand} •••• {payment.card.last4}
                </dd>
              </>
            )}
            {payment.authorization_code && (
              <>
                <dt>Autorización</dt>
                <dd className="mono">{payment.authorization_code}</dd>
              </>
            )}
            {payment.decline_code && (
              <>
                <dt>Rechazo</dt>
                <dd className="mono">{payment.decline_code}</dd>
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
          <AutoRefresh
            active={!isFinal(payment.status)}
            label="Actualizando mientras se procesa…"
            every={1000}
            slowAfter={30_000}
            slowEvery={5000}
          />
        </section>

        <section className={`card ${styles.history}`}>
          <h2 className={styles.historyTitle}>Historia</h2>
          <ol className={styles.timeline}>
            {history.map((e, i) => {
              const { title, dot, chips } = describe(e);
              return (
                <li key={i} className={styles.event}>
                  <span className={`${styles.dot} ${styles[dot] ?? ""}`} aria-hidden="true" />
                  <time className={`mono ${styles.time}`} dateTime={e.at}>
                    {formatTime(e.at)}
                  </time>
                  <span className={styles.eventStatus}>{title}</span>
                  <span className={styles.chips}>
                    {chips.map((c) => (
                      <span key={c} className={styles.chip}>
                        {c}
                      </span>
                    ))}
                  </span>
                </li>
              );
            })}
          </ol>
        </section>
      </div>
    </>
  );
}
