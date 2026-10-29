import type { Metadata } from "next";
import Link from "next/link";

import { getBook, getFaults } from "@/lib/acquirer";
import { formatAmount, formatDateTime, shortId } from "@/lib/format";

import list from "../page.module.css";
import { FaultsForm } from "./faults-form";
import styles from "./page.module.css";

export const metadata: Metadata = { title: "Adquirente" };

export const dynamic = "force-dynamic";

const OUTCOMES = { approved: "aprobada", declined: "rechazada", none: "nada" } as const;

export default async function AcquirerPage() {
  const [faults, book] = await Promise.all([getFaults(), getBook(20)]);

  return (
    <>
      <div className={list.heading}>
        <h1>Adquirente simulado</h1>
        <p className="muted">
          Sus fallas afectan a cada autorización y reverso. Con el adquirente caído, el circuit breaker se abre tras 5
          fallas y los pagos nuevos reciben 503.
        </p>
      </div>

      <div className={styles.layout}>
        <FaultsForm faults={faults} />

        <section className={`card ${list.tableWrap}`}>
          <h2 className={styles.title}>Su libro: las últimas 20 claves</h2>
          {book.length === 0 ? (
            <p className={`muted ${styles.title}`}>Todavía no recibe ninguna autorización.</p>
          ) : (
            <table className={list.table}>
              <thead>
                <tr>
                  <th>Fecha</th>
                  <th>Clave = pago</th>
                  <th>Resultado</th>
                  <th className={list.num}>Monto</th>
                  <th>Retenido</th>
                </tr>
              </thead>
              <tbody>
                {book.map((e) => (
                  <tr key={e.key}>
                    <td className="muted">{formatDateTime(e.created_at)}</td>
                    <td>
                      <Link href={`/payments/${e.key}`} className={`mono ${list.id}`}>
                        {shortId(e.key)}
                      </Link>
                    </td>
                    <td>
                      {OUTCOMES[e.outcome]}
                      {e.reversed && <span className={list.reason}>reversada</span>}
                    </td>
                    <td className={list.num}>
                      {e.amount && e.currency ? (
                        <>
                          {formatAmount(e.amount, e.currency)} <span className="muted">{e.currency}</span>
                        </>
                      ) : null}
                    </td>
                    <td>{e.live ? "sí" : "no"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </section>
      </div>
    </>
  );
}
