import Link from "next/link";

import { StatusBadge } from "@/components/status-badge";
import { listPayments } from "@/lib/api";
import { formatAmount, formatDateTime, shortId } from "@/lib/format";
import type { Status } from "@/lib/statuses";

import feedback from "./feedback.module.css";
import styles from "./page.module.css";

type Props = { merchantId: string; status?: Status; limit: number };

export async function PaymentsTable({ merchantId, status, limit }: Props) {
  const payments = await listPayments({ merchantId: merchantId || undefined, status, limit });

  if (payments.length === 0) {
    return (
      <div className={`card ${styles.empty}`}>
        <p className={styles.emptyTitle}>No hay pagos con estos filtros</p>
        <p className="muted">
          Crea uno con <Link href="/payments/new">un pago de prueba</Link>.
        </p>
      </div>
    );
  }

  return (
    <div className={`card ${styles.tableWrap}`}>
      <table className={styles.table}>
        <thead>
          <tr>
            <th className={styles.wide}>Fecha</th>
            <th>Pago</th>
            <th className={styles.wide}>Comercio</th>
            <th className={styles.num}>Monto</th>
            <th>Estado</th>
          </tr>
        </thead>
        <tbody>
          {payments.map((p) => (
            <tr key={p.transaction_id}>
              <td className={`muted ${styles.wide}`}>{formatDateTime(p.created_at)}</td>
              <td>
                <Link href={`/payments/${p.transaction_id}`} className={`mono ${styles.id}`}>
                  {shortId(p.transaction_id)}
                </Link>
              </td>
              <td className={styles.wide}>{p.merchant_id}</td>
              <td className={styles.num}>
                {formatAmount(p.amount, p.currency)} <span className="muted">{p.currency}</span>
              </td>
              <td>
                <span className={styles.status}>
                  <StatusBadge status={p.status} />
                  {p.reason && <span className={`${styles.reason} ${styles.wide}`}>{p.reason}</span>}
                </span>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export function PaymentsTableSkeleton() {
  return <div className={feedback.skeleton} role="status" aria-label="Cargando" />;
}
