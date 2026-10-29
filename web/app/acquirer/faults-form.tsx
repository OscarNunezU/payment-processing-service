"use client";

import { useActionState } from "react";

import type { Faults } from "@/lib/acquirer";

import { saveFaults, type State } from "./actions";
import styles from "./page.module.css";

export function FaultsForm({ faults }: { faults: Faults }) {
  const [state, action, pending] = useActionState<State, FormData>(saveFaults, {});

  // Mounted again with each saved value, since inputs keep what was typed.
  return (
    <form key={JSON.stringify(faults)} action={action} className={`card ${styles.form}`}>
      <label className={styles.field}>
        Latencia agregada, en ms
        <input className="input" name="latency_ms" type="number" min={0} max={10000} defaultValue={faults.latency_ms} />
      </label>
      <label className={styles.field}>
        Proporción de 503 sin procesar
        <input className="input" name="error_rate" type="number" min={0} max={1} step={0.05} defaultValue={faults.error_rate} />
      </label>
      <label className={styles.field}>
        Proporción procesada y respondida tarde
        <input className="input" name="timeout_rate" type="number" min={0} max={1} step={0.05} defaultValue={faults.timeout_rate} />
      </label>
      <label className={styles.check}>
        <input name="down" type="checkbox" defaultChecked={faults.down} />
        Caído: el puerto público rechaza las conexiones
      </label>
      {state.error && (
        <p className={styles.error} role="alert">
          {state.error}
        </p>
      )}
      <div className={styles.footer}>
        <button type="submit" className="button" disabled={pending}>
          {pending ? "Guardando…" : "Guardar"}
        </button>
      </div>
    </form>
  );
}
