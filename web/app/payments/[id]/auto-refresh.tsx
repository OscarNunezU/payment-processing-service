"use client";

import { useRouter } from "next/navigation";
import { useEffect } from "react";

import styles from "./page.module.css";

// While the payment is in flight, asks the server to render the page again
// every second; once the status is final the page passes active=false and the
// refreshing stops.
export function AutoRefresh({ active }: { active: boolean }) {
  const router = useRouter();

  useEffect(() => {
    if (!active) return;
    const timer = setInterval(() => router.refresh(), 1000);
    return () => clearInterval(timer);
  }, [active, router]);

  if (!active) return null;
  return (
    <p className={styles.live} aria-live="polite">
      Actualizando mientras se procesa…
    </p>
  );
}
