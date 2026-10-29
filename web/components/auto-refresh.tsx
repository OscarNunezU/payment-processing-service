"use client";

import { useRouter } from "next/navigation";
import { useEffect } from "react";

import styles from "./auto-refresh.module.css";

type Props = {
  active: boolean;
  label: string;
  // every is the wait between refreshes, in milliseconds. After slowAfter,
  // when given, the wait becomes slowEvery: a payment that is not final in
  // half a minute is waiting for its recovery, which takes longer.
  every?: number;
  slowAfter?: number;
  slowEvery?: number;
};

// While active, asks the server to render the page again, and says so with
// label. A hidden tab waits, and refreshes as soon as it is shown again. A
// payment's page turns it off once the payment is final.
export function AutoRefresh({ active, label, every = 1000, slowAfter, slowEvery = 5000 }: Props) {
  const router = useRouter();

  useEffect(() => {
    if (!active) return;
    const started = Date.now();
    let timer: ReturnType<typeof setTimeout> | undefined;

    const wait = () => (slowAfter !== undefined && Date.now() - started > slowAfter ? slowEvery : every);
    function schedule() {
      clearTimeout(timer);
      if (document.hidden) return;
      timer = setTimeout(() => {
        router.refresh();
        schedule();
      }, wait());
    }
    function onVisibility() {
      if (document.hidden) {
        clearTimeout(timer);
        return;
      }
      router.refresh();
      schedule();
    }

    schedule();
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [active, every, slowAfter, slowEvery, router]);

  if (!active) return null;
  return (
    <p className={styles.live} aria-live="polite">
      {label}
    </p>
  );
}
