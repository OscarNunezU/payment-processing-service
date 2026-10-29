// The admin port of the acquirer simulator: its faults and its book. Only the
// server reaches it; the browser only talks to the public port, to tokenize.
import "server-only";

const ADMIN_URL = process.env.ACQUIRER_ADMIN_URL ?? "http://localhost:8091";

export type Faults = { latency_ms: number; error_rate: number; timeout_rate: number; down: boolean };

export type BookEntry = {
  key: string;
  outcome: "approved" | "declined" | "none";
  amount?: number;
  currency?: string;
  reversed: boolean;
  live: boolean;
  created_at: string;
};

export async function getFaults(): Promise<Faults> {
  const res = await fetch(`${ADMIN_URL}/admin/faults`, { cache: "no-store" });
  if (!res.ok) throw new Error(`the acquirer answered ${res.status} to GET /admin/faults`);
  return (await res.json()) as Faults;
}

// putFaults returns the simulator's error message when it refuses the faults.
export async function putFaults(faults: Faults): Promise<string | null> {
  const res = await fetch(`${ADMIN_URL}/admin/faults`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(faults),
    cache: "no-store",
  });
  if (res.ok) return null;
  const body = (await res.json().catch(() => null)) as { error?: { message?: string } } | null;
  return body?.error?.message ?? `the acquirer answered ${res.status}`;
}

// getBook returns the newest entries first.
export async function getBook(limit: number): Promise<BookEntry[]> {
  const res = await fetch(`${ADMIN_URL}/admin/book`, { cache: "no-store" });
  if (!res.ok) throw new Error(`the acquirer answered ${res.status} to GET /admin/book`);
  const { data } = (await res.json()) as { data: BookEntry[] };
  return data.slice(-limit).reverse();
}
