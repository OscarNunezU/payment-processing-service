---
title: "ADR 0009: Reintentos de errores temporales del adquirente"
status: accepted
date: 2026-10-15
deciders: oscar
---

# ADR 0009: Reintentos de errores temporales del adquirente

## Contexto

El enunciado pide considerar que el adquirente responda con errores temporales. Hasta ahora, cualquier error del adquirente dejaba el pago en `FAILED`, aunque un segundo intento segundos después lo habría autorizado. El caso más delicado es el timeout: el adquirente pudo haber aprobado sin que la respuesta llegara.

## Decisión

- **Se reintenta por defecto, y se falla de inmediato solo lo que se sabe permanente.** Reintentar es seguro porque cada llamada lleva el id de la transacción como `Idempotency-Key`, así que el adquirente nunca autoriza dos veces. Son permanentes un `4xx` del adquirente, porque rechazó la request misma y reenviarla da lo mismo (salvo `408` y `429`, que pueden pasar en otro intento), y una tarjeta que no se puede descifrar. Un timeout, un `5xx`, un `408` o `429`, una conexión caída o una respuesta ilegible son temporales.
- **El reintento pasa por la cola.** El pago sigue `PROCESSING`, con su tarjeta, y vuelve a estar disponible después de la espera (`available_at`, el mismo mecanismo del lease). El worker queda libre para otros pagos y el reintento sobrevive a un reinicio.
- **Backoff exponencial con jitter.** Son máximo 3 intentos. La espera se duplica desde una base de 2 s, con la mitad aleatoria: 1-2 s y luego 2-4 s. El jitter evita que los pagos que fallaron juntos vuelvan todos al mismo instante. Si el tercer intento tampoco obtiene respuesta, el pago termina `FAILED` con motivo `acquirer_unavailable`; un rechazo permanente termina con `acquirer_rejected`.
- **El timeout ambiguo se resuelve solo:** el reintento con la misma clave recibe la respuesta que el adquirente ya tenía. El adquirente simulado tiene una tarjeta que lo reproduce.
- Cada intento sin respuesta queda en la historia: como `PROCESSING` con su motivo si habrá otro, como prevé la ADR 0008, o como el `FAILED` final. El contador de intentos incluye también las tomas después de que venció un lease (un worker que murió a mitad de camino), porque tampoco se supo su resultado.
- Los valores (3 intentos, base de 2 s) son supuestos: 3 intentos en unos 6 s cubren una falla de segundos sin retener el pago. En producción se ajustarían al acuerdo de servicio del adquirente.

## Consecuencias

**Positivas**
- Una falla breve del adquirente ya no hace fallar el pago.
- Mientras el adquirente responda dentro de los intentos, un timeout no termina ni en un cobro doble ni en un pago `FAILED` que en realidad se aprobó.
- Los pagos que esperan su reintento no ocupan workers.

**Negativas**
- Un pago puede tardar unos 6 s más, más la duración de cada intento, en llegar a su estado final.
- Si ningún intento obtiene respuesta, el pago queda `FAILED acquirer_unavailable`, aunque el adquirente pudo haber aprobado alguno. En producción eso se resuelve conciliando con el adquirente o anulando el cobro.
- Una caída larga del adquirente agota en pocos segundos los intentos de los pagos en curso.

## Alternativas consideradas

- **Reintentar dentro del worker con `sleep`**: ocupa un worker durante la espera, y el reintento se pierde si el proceso se reinicia.
- **Fallar por defecto y reintentar solo los errores conocidos como temporales**: un error inesperado haría fallar un pago que se podía reintentar sin riesgo.
- **Backoff sin jitter**: los pagos que fallaron juntos volverían todos a la vez, justo cuando el adquirente se está recuperando.
- **Full jitter (una espera entre 0 y el máximo)**: reparte más, pero puede reintentar de inmediato; con solo 3 intentos conviene una espera mínima.
- **Reintentar hasta que el adquirente responda**: un pago podría quedar en `PROCESSING` indefinidamente.
