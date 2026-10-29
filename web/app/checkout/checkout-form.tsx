"use client";

import { useActionState, useCallback, useRef, useState } from "react";

import { pay, type State, type Values } from "./actions";
import { CardFields, type CardFieldsHandle } from "./card-fields";
import styles from "./page.module.css";

type Props = {
  idempotencyKey: string;
  orderId: string;
  acquirer: { url: string; publishableKey: string };
};

export function CheckoutForm({ idempotencyKey, orderId, acquirer }: Props) {
  // Controlled, and never mounted again: the card lives in the acquirer's
  // iframe, which keeps the card and its token while the page lives, so
  // sending the form again sends the same request.
  const [amount, setAmount] = useState("15000");
  const [currency, setCurrency] = useState("CLP");
  const [complete, setComplete] = useState(false);
  const card = useRef<CardFieldsHandle>(null);
  const onComplete = useCallback((c: boolean) => setComplete(c), []);

  const [state, action, pending] = useActionState<State, FormData>(async (_previous, form) => {
    const values: Values = { amount: String(form.get("amount") ?? ""), currency: String(form.get("currency") ?? "") };
    const result = await card.current?.tokenize();
    if (!result || "error" in result) {
      return { tokenError: result?.error ?? "El formulario de la tarjeta no está listo." };
    }
    form.set("card_token", result.token);
    return pay(values, form);
  }, {});

  return (
    <form action={action} className={`card ${styles.form}`}>
      <input type="hidden" name="idempotency_key" value={idempotencyKey} />
      <input type="hidden" name="order_id" value={orderId} />

      <div className={styles.row}>
        <label className={styles.field}>
          Monto, en la unidad mínima
          <input
            className="input"
            name="amount"
            type="number"
            min={1}
            step={1}
            value={amount}
            onChange={(e) => setAmount(e.target.value)}
            required
          />
        </label>
        <label className={styles.field}>
          Moneda
          <select className="input" name="currency" value={currency} onChange={(e) => setCurrency(e.target.value)}>
            <option value="CLP">CLP</option>
            <option value="USD">USD (centavos)</option>
          </select>
        </label>
      </div>

      <CardFields ref={card} acquirer={acquirer} onComplete={onComplete} />

      {state.tokenError && (
        <div className={styles.error} role="alert">
          <p>
            <strong>No se pudo tokenizar la tarjeta</strong>
          </p>
          <p>{state.tokenError} El pago no se envió.</p>
        </div>
      )}

      {state.rejected && (
        <div className={styles.error} role="alert">
          <p>
            <strong>
              {state.rejected.httpStatus === 503 ? "El adquirente no está disponible" : "La API rechazó el pago"}
            </strong>{" "}
            <span className="mono">
              {state.rejected.httpStatus} {state.rejected.code}
            </span>
          </p>
          <p>{state.rejected.message}</p>
          {state.rejected.httpStatus === 503 && (
            <p>
              El pago no se creó. Puedes enviarlo de nuevo
              {state.rejected.retryAfter ? ` en ${state.rejected.retryAfter} s` : ""}.
            </p>
          )}
          {state.rejected.code === "idempotency_key_reused" && (
            <p>Esta clave ya pagó con otros datos. Para un pago nuevo, vuelve a abrir el checkout.</p>
          )}
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

      {state.unconfirmed && (
        <div className={styles.error} role="alert">
          <p>
            <strong>No se pudo confirmar el pago</strong>
          </p>
          <p>
            El servicio no pudo decir si el pago quedó creado. Vuelve a enviarlo
            {state.unconfirmed.retryAfter ? ` en ${state.unconfirmed.retryAfter} s` : ""}: con la misma clave no se crea
            un duplicado, y si ya existía, recibirás ese mismo pago.
          </p>
        </div>
      )}

      <div className={styles.footer}>
        <p className={styles.key}>
          Orden <span className="mono">{orderId}</span> · clave de idempotencia{" "}
          <span className="mono">{idempotencyKey.slice(0, 8)}</span>: enviar dos veces este formulario no crea dos
          pagos.
        </p>
        <button type="submit" className="button" disabled={pending || !complete}>
          {pending ? "Pagando…" : "Pagar"}
        </button>
      </div>
    </form>
  );
}
