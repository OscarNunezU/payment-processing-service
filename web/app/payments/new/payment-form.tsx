"use client";

import { useActionState } from "react";
import { useFormStatus } from "react-dom";

import { createTestPayment, type FormState, type FormValues } from "./actions";
import styles from "./page.module.css";

// The acquirer mock decides by card number (see the README).
const TEST_CARDS = [
  { number: "4111111111111111", label: "Se aprueba" },
  { number: "4000000000000002", label: "Rechazada: card_declined" },
  { number: "4000000000009995", label: "Rechazada: insufficient_funds" },
  { number: "4000000000000127", label: "503 la primera vez, aprobada al reintentar" },
  { number: "4000000000000135", label: "Lenta: timeout y aprobada al reintentar" },
  { number: "4000000000000119", label: "503 siempre: 3 intentos y FAILED" },
  { number: "4000000000000143", label: "El adquirente rechaza la request" },
];

const DEFAULTS: FormValues = {
  merchant_id: "merchant-demo",
  amount: "15000",
  currency: "CLP",
  card: TEST_CARDS[0].number,
};

function SubmitButton() {
  const { pending } = useFormStatus();
  return (
    <button type="submit" className="button" disabled={pending}>
      {pending ? "Pagando…" : "Pagar"}
    </button>
  );
}

export function PaymentForm({ idempotencyKey }: { idempotencyKey: string }) {
  const [state, action] = useActionState<FormState, FormData>(createTestPayment, {});
  const values = state.values ?? DEFAULTS;

  // React clears the form after the action runs, and a select ignores a new
  // defaultValue once mounted, so the form mounts again with what was sent.
  return (
    <form key={JSON.stringify(values)} action={action} className={`card ${styles.form}`}>
      <input type="hidden" name="idempotency_key" value={idempotencyKey} />

      <label className={styles.field}>
        Comercio
        <input className="input" name="merchant_id" defaultValue={values.merchant_id} required />
      </label>

      <div className={styles.row}>
        <label className={styles.field}>
          Monto, en la unidad mínima
          <input className="input" name="amount" type="number" min={1} step={1} defaultValue={values.amount} required />
        </label>
        <label className={styles.field}>
          Moneda
          <select className="input" name="currency" defaultValue={values.currency}>
            <option value="CLP">CLP</option>
            <option value="USD">USD (centavos)</option>
          </select>
        </label>
      </div>

      <label className={styles.field}>
        Tarjeta de prueba
        <select className="input" name="card" defaultValue={values.card}>
          {TEST_CARDS.map((card) => (
            <option key={card.number} value={card.number}>
              {`•••• ${card.number.slice(-4)} · ${card.label}`}
            </option>
          ))}
        </select>
      </label>

      {state.rejected && (
        <div className={styles.error} role="alert">
          <p>
            <strong>La API rechazó el pago</strong> <span className="mono">{state.rejected.code}</span>
          </p>
          <p>{state.rejected.message}</p>
          {state.rejected.fields && (
            <ul>
              {state.rejected.fields.map((f) => (
                <li key={f.field}>
                  <span className="mono">{f.field}</span> {f.message}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}

      {state.unavailable && (
        <div className={styles.error} role="alert">
          <p>
            <strong>No se pudo confirmar el pago</strong>
          </p>
          <p>
            La API no respondió o falló. Revisa que esté corriendo y vuelve a enviarlo: con la misma clave no se crea un
            pago duplicado.
          </p>
        </div>
      )}

      <div className={styles.footer}>
        <p className={styles.key}>
          Clave de idempotencia <span className="mono">{idempotencyKey.slice(0, 8)}</span>: enviar dos veces este
          formulario no crea dos pagos.
        </p>
        <SubmitButton />
      </div>
    </form>
  );
}
