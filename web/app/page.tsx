import type { Metadata } from "next";
import { Suspense } from "react";

import { Filters } from "@/components/filters";
import { isStatus } from "@/lib/statuses";

import styles from "./page.module.css";
import { PaymentsTable, PaymentsTableSkeleton } from "./payments-table";

export const metadata: Metadata = { title: "Pagos" };

const LIMIT = 25;

type SearchParams = Promise<Record<string, string | string[] | undefined>>;

function first(value: string | string[] | undefined): string {
  return (Array.isArray(value) ? value[0] : value)?.trim() ?? "";
}

export default async function PaymentsPage({ searchParams }: { searchParams: SearchParams }) {
  const params = await searchParams;
  const orderId = first(params.order_id);
  const requested = first(params.status);
  const status = isStatus(requested) ? requested : undefined;
  const startingAfter = first(params.starting_after);

  // New keys with every filter: the form mounts again with the URL's values
  // after back, forward or "Limpiar", and the table shows its skeleton while
  // the API answers, which a route loading.tsx doesn't do when only the search
  // params change. Siblings need different keys, or React duplicates them.
  const filters = `${orderId}|${status ?? ""}|${startingAfter}`;

  return (
    <>
      <div className={styles.heading}>
        <h1>Pagos</h1>
        <p className="muted">Los del comercio de prueba, del más reciente al más antiguo</p>
      </div>

      <Filters key={`form:${filters}`} orderId={orderId} status={status ?? ""} />

      <Suspense key={`table:${filters}`} fallback={<PaymentsTableSkeleton />}>
        <PaymentsTable orderId={orderId} status={status} startingAfter={startingAfter} limit={LIMIT} />
      </Suspense>
    </>
  );
}
