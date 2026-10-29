import type { Metadata } from "next";
import Link from "next/link";

import { AutoRefresh } from "@/components/auto-refresh";
import { formatDateTime, shortId } from "@/lib/format";
import { failing, received } from "@/lib/webhooks";

import styles from "../page.module.css";
import { toggleFailing } from "./actions";
import panel from "./page.module.css";

export const metadata: Metadata = { title: "Webhooks" };

// The list lives in this server's memory and changes with every webhook.
export const dynamic = "force-dynamic";

const VERDICTS = { valid: "válida", invalid: "inválida", stale: "vencida" } as const;

export default function WebhooksPage() {
  const list = received();
  const down = failing();

  return (
    <>
      <div className={styles.heading}>
        <h1>Webhooks recibidos</h1>
        <p className="muted">
          Los que el servicio envió a <span className="mono">/api/webhooks</span>, con la firma verificada. Se guardan
          en memoria: los últimos 100.
        </p>
      </div>

      <form action={toggleFailing} className={`card ${panel.control}`}>
        <input type="hidden" name="failing" value={down ? "off" : "on"} />
        <p>
          {down ? (
            <>
              <strong>El endpoint responde 500.</strong> El servicio reintenta cada webhook, desde 10 s hasta cada
              hora, durante 24 horas.
            </>
          ) : (
            "El endpoint responde 200 a cada webhook con firma válida."
          )}
        </p>
        <button type="submit" className="button button-ghost">
          {down ? "Volver a responder 200" : "Simular el endpoint caído"}
        </button>
      </form>

      {list.length === 0 ? (
        <div className={`card ${styles.empty}`}>
          <p className={styles.emptyTitle}>Todavía no llega ningún webhook</p>
          <p className="muted">
            Cada pago que termina produce uno. Prueba con <Link href="/checkout">un pago de prueba</Link>.
          </p>
        </div>
      ) : (
        <div className={`card ${styles.tableWrap}`}>
          <table className={styles.table}>
            <thead>
              <tr>
                <th>Recibido</th>
                <th>Evento</th>
                <th>Pago</th>
                <th className={styles.wide}>Id del evento</th>
                <th>Firma</th>
                <th className={styles.num}>Veces</th>
              </tr>
            </thead>
            <tbody>
              {list.map((w, i) => (
                <tr key={`${w.id}-${i}`}>
                  <td className="muted">{formatDateTime(w.lastAt)}</td>
                  <td className="mono">{w.type}</td>
                  <td>
                    {w.paymentId && (
                      <Link href={`/payments/${w.paymentId}`} className={`mono ${styles.id}`}>
                        {shortId(w.paymentId)}
                      </Link>
                    )}
                  </td>
                  <td className={`mono muted ${styles.wide}`}>{w.id}</td>
                  <td className={w.verdict === "valid" ? panel.valid : panel.invalid}>{VERDICTS[w.verdict]}</td>
                  <td className={styles.num}>{w.times}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <AutoRefresh active label="Actualizando cada 2 s" every={2000} />
    </>
  );
}
