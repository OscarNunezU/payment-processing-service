import Form from "next/form";
import Link from "next/link";

import { STATUSES } from "@/lib/statuses";

import styles from "./filters.module.css";

// A GET form: the filters live in the URL, so a filtered list can be shared,
// and it works before JavaScript loads. next/form navigates without a reload.
export function Filters({ merchantId, status }: { merchantId: string; status: string }) {
  const filtered = merchantId !== "" || status !== "";

  return (
    <Form action="/" className={`card ${styles.filters}`}>
      <label className={styles.field}>
        Comercio
        <input className="input" name="merchant_id" defaultValue={merchantId} placeholder="Todos" />
      </label>
      <label className={styles.field}>
        Estado
        <select className="input" name="status" defaultValue={status}>
          <option value="">Todos</option>
          {STATUSES.map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
      </label>
      <div className={styles.actions}>
        <button type="submit" className="button button-ghost">
          Filtrar
        </button>
        {filtered && (
          <Link href="/" className={styles.clear}>
            Limpiar
          </Link>
        )}
      </div>
    </Form>
  );
}
