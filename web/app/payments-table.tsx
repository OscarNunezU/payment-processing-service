import Link from "next/link";

import { StatusBadge } from "@/components/status-badge";
import { listPayments } from "@/lib/api";
import { formatAmount, formatDateTime, shortId } from "@/lib/format";
import type { Status } from "@/lib/statuses";

import feedback from "./feedback.module.css";
import styles from "./page.module.css";

type Props = { orderId: string; status?: Status; startingAfter: string; limit: number };

export async function PaymentsTable({ orderId, status, startingAfter, limit }: Props) {
  const page = await listPayments({
    orderId: orderId || undefined,
    status,
    startingAfter: startingAfter || undefined,
    limit,
  });

  if (page.data.length === 0) {
    return (
      <div className={`card ${styles.empty}`}>
        <p className={styles.emptyTitle}>No hay pagos con estos filtros</p>
        <p className="muted">
          Crea uno con <Link href="/checkout">un pago de prueba</Link>.
        </p>
      </div>
    );
  }

  // The next page starts after the last payment of this one, with the same
  // filters (RFC 0001, section 11).
  const next = new URLSearchParams({ starting_after: page.data[page.data.length - 1].id });
  if (orderId) next.set("order_id", orderId);
  if (status) next.set("status", status);

  return (
    <>
      <div className={`card ${styles.tableWrap}`}>
        <table className={styles.table}>
          <thead>
            <tr>
              <th className={styles.wide}>Fecha</th>
              <th>Pago</th>
              <th className={styles.wide}>Orden</th>
              <th className={styles.num}>Monto</th>
              <th>Estado</th>
            </tr>
          </thead>
          <tbody>
            {page.data.map((p) => (
              <tr key={p.id}>
                <td className={`muted ${styles.wide}`}>{formatDateTime(p.created_at)}</td>
                <td>
                  <Link href={`/payments/${p.id}`} className={`mono ${styles.id}`}>
                    {shortId(p.id)}
                  </Link>
                </td>
                <td className={`mono ${styles.wide}`}>{p.order_id}</td>
                <td className={styles.num}>
                  {formatAmount(p.amount, p.currency)} <span className="muted">{p.currency}</span>
                </td>
                <td>
                  <span className={styles.status}>
                    <StatusBadge status={p.status} />
                    {(p.reason || p.decline_code) && (
                      <span className={`${styles.reason} ${styles.wide}`}>{p.reason || p.decline_code}</span>
                    )}
                  </span>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {page.has_more && (
        <p className={styles.more}>
          <Link href={`/?${next}`} className="button button-ghost">
            Siguientes →
          </Link>
        </p>
      )}
    </>
  );
}

export function PaymentsTableSkeleton() {
  return <div className={feedback.skeleton} role="status" aria-label="Cargando" />;
}
