"use client";

import { type Ref, useEffect, useImperativeHandle, useRef, useState } from "react";

import styles from "./page.module.css";

export type Tokenized = { token: string; brand: string; last4: string } | { error: string };

export type CardFieldsHandle = { tokenize: () => Promise<Tokenized> };

type Props = {
  acquirer: { url: string; publishableKey: string };
  // onComplete says whether the fields hold a whole card.
  onComplete: (complete: boolean) => void;
  ref: Ref<CardFieldsHandle>;
};

// How long the acquirer's page may take to say it is ready, and to answer.
const READY_WITHIN = 5000;
const ANSWER_WITHIN = 15000;

// CardFields embeds the acquirer's card fields (RFC 0001, section 12). The
// cardholder types the card on the acquirer's origin: this page never reads
// it, and only gets a token back. Every message is checked against the
// acquirer's origin and the iframe's window, so no other page can answer.
export function CardFields({ acquirer, onComplete, ref }: Props) {
  const frame = useRef<HTMLIFrameElement>(null);
  const [src, setSrc] = useState<string>();
  const [height, setHeight] = useState(220);
  const [state, setState] = useState<"loading" | "ready" | "unavailable">("loading");
  const target = new URL(acquirer.url).origin;

  // The page's own origin is known only in the browser, so the iframe is
  // added after the first render.
  useEffect(() => {
    const theme = document.documentElement.dataset.theme ?? "";
    const params = new URLSearchParams({ key: acquirer.publishableKey, origin: window.location.origin, theme });
    setSrc(`${acquirer.url}/v1/fields?${params}`);
  }, [acquirer.url, acquirer.publishableKey]);

  useEffect(() => {
    function onMessage(e: MessageEvent) {
      if (e.origin !== target || e.source !== frame.current?.contentWindow) return;
      if (e.data?.type === "ready") {
        setState("ready");
        setHeight(e.data.height);
      }
      if (e.data?.type === "resize") setHeight(e.data.height);
      if (e.data?.type === "change") onComplete(Boolean(e.data.complete));
    }
    window.addEventListener("message", onMessage);
    // With the acquirer down, its page never loads, and never says so.
    const timer = setTimeout(() => setState((s) => (s === "loading" ? "unavailable" : s)), READY_WITHIN);
    return () => {
      window.removeEventListener("message", onMessage);
      clearTimeout(timer);
    };
  }, [target, onComplete]);

  useImperativeHandle(
    ref,
    () => ({
      tokenize: () =>
        new Promise<Tokenized>((resolve) => {
          const win = frame.current?.contentWindow;
          if (!win || state !== "ready") {
            resolve({ error: "El formulario de la tarjeta no está listo." });
            return;
          }
          const timer = setTimeout(() => finish({ error: "El adquirente no responde." }), ANSWER_WITHIN);
          function onAnswer(e: MessageEvent) {
            if (e.origin !== target || e.source !== win) return;
            if (e.data?.type === "token") finish({ token: e.data.token, brand: e.data.brand, last4: e.data.last4 });
            if (e.data?.type === "error") finish({ error: e.data.message });
          }
          function finish(result: Tokenized) {
            clearTimeout(timer);
            window.removeEventListener("message", onAnswer);
            resolve(result);
          }
          window.addEventListener("message", onAnswer);
          win.postMessage({ type: "tokenize" }, target);
        }),
    }),
    [state, target],
  );

  return (
    <div className={styles.field}>
      {src && (
        <iframe
          ref={frame}
          src={src}
          title="Datos de la tarjeta"
          className={styles.cardFrame}
          style={{ height }}
          hidden={state === "unavailable"}
        />
      )}
      {state === "unavailable" && (
        <p className={styles.error} role="alert">
          No se pudo cargar el formulario de la tarjeta: el adquirente no responde.
        </p>
      )}
    </div>
  );
}
