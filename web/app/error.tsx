"use client";

import { useRouter } from "next/navigation";
import { startTransition } from "react";

import styles from "./feedback.module.css";

// Next requires error boundaries to be client components. reset() alone renders
// again what the server already sent, error included, so the retry also asks
// the server for the page again.
export default function ErrorPage({ reset }: { error: Error; reset: () => void }) {
  const router = useRouter();

  function retry() {
    startTransition(() => {
      router.refresh();
      reset();
    });
  }

  return (
    <div className={`card ${styles.message}`}>
      <p className={styles.title}>No pudimos hablar con la API</p>
      <p className="muted">Revisa que esté corriendo e inténtalo de nuevo.</p>
      <button type="button" className="button button-ghost" onClick={retry}>
        Reintentar
      </button>
    </div>
  );
}
