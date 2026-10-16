---
title: "ADR 0007: Idempotencia por comercio"
status: accepted
date: 2026-10-15
deciders: oscar
---

# ADR 0007: Idempotencia por comercio

## Contexto

Si al comercio se le corta la conexión después de enviar un pago, no sabe si se creó y lo reintenta. Sin protección, cada reintento crea un pago nuevo y el cliente paga dos veces. El enunciado pide evitar la duplicidad de transacciones.

## Decisión

- `POST /payments` exige el header `Idempotency-Key`, de 1 a 255 caracteres imprimibles. Es el estándar de la industria (Stripe, Adyen y el borrador de la IETF) y el mismo header que usa la API de OpenFactura.
- La clave es única por comercio: índice único sobre `(merchant_id, idempotency_key)`.
- **Primero se intenta guardar, y la clave se lee solo si ya existía.** El `INSERT ... ON CONFLICT DO NOTHING` intenta guardar el pago; si la clave ya existe, no guarda nada y se lee el pago que la tiene. Quien impide el duplicado es el índice único: consultar la clave antes no lo reemplaza, porque dos requests pueden consultar a la vez y no ver nada. Un pago nuevo, el caso común, cuesta un solo viaje a la base.
- **Es el camino que la industria usa cuando la request solo escribe localmente.** La implementación de referencia de Stripe consulta la clave primero, dentro de una transacción `SERIALIZABLE`, con bloqueos y puntos de recuperación, porque su request cobra y envía correos en la misma llamada; su autor recomienda, cuando el endpoint solo modifica estado local, mapear la request a una transacción. Aquí el POST solo escribe una fila: el cobro va después, en el worker, con su propia clave (ADR 0005). Escribir primero y leer si hay conflicto es también el patrón de AWS Lambda Powertools, con una escritura condicional.
- Misma clave y mismo pago: se devuelve el pago original con su estado actual, `202` y el header `Idempotent-Replayed: true`.
- Misma clave y otro pago: `422 idempotency_key_reused`. Para distinguirlos se guarda una huella SHA-256 del monto, la moneda, los últimos 4 dígitos y el vencimiento; nunca del número completo ni del CVV.
- Si una regla rechaza el pago, se busca la clave antes de responder: un reintento legítimo recibe su pago aunque un límite haya cambiado o la tarjeta haya vencido. Si esa búsqueda falla, se responde `500` y no `422`, porque el pago podría existir.
- Una request rechazada (`400` o `422`) no guarda nada, así que no gasta la clave.
- Dos requests simultáneas con la misma clave: el índice único hace esperar a la segunda hasta que la primera termina, y la segunda recibe el pago de la primera.
- La clave se guarda en la fila del pago y no vence.

## Consecuencias

**Positivas**
- Un reintento nunca crea un segundo pago, ni siquiera con requests simultáneas.
- Para el comercio, reintentar es igual que el primer intento.
- Un pago nuevo es una sola escritura y un solo viaje a la base. Medido en local, en una prueba A/B intercalada con la misma carga, se crean un 46 % más de pagos por segundo que consultando la clave antes de cada `INSERT` (~9.100 contra ~6.200).

**Negativas**
- Un cliente que no envía la clave recibe `400`.
- Para reintentar un pago `FAILED` hay que usar una clave nueva: la misma clave devuelve el mismo resultado.
- Un reintento cuesta dos viajes a la base (el `INSERT` que no guarda y la lectura del pago) y rinde un 43 % menos que con la consulta previa (~8.700 contra ~15.300 por segundo). Se acepta porque los reintentos son la excepción.
- Dos tarjetas distintas con los mismos últimos 4 dígitos y el mismo vencimiento se tomarían como el mismo pago. El error va hacia el lado seguro: se devuelve el pago original y no se cobra dos veces.

## Alternativas consideradas

- **Consultar la clave antes de guardar, como la implementación de referencia de Stripe**: tiene sentido cuando la request hace varios pasos con efectos externos y necesita bloquear la clave mientras corre. Aquí la request es una sola escritura, así que solo le sumaría un viaje a la base a cada pago nuevo, y el árbitro seguiría siendo el índice único.
- **Clave en el body**: mezcla los metadatos del transporte con los datos del pago.
- **Clave opcional**: sin clave no hay protección, y en pagos un duplicado es un cobro doble.
- **Guardar la respuesta original exacta**: exige almacenar respuestas, y su estado quedaría desactualizado.
- **Huella con un HMAC del número completo**: es exacta, pero exige otra clave secreta que administrar.
- **`409` mientras la primera request sigue en curso** (borrador de la IETF): aquí "en curso" es solo el `INSERT`, que dura milisegundos; esperar evita un error que el comercio tendría que reintentar.
- **Vencimiento a las 24 horas**: Stripe lo hace por almacenamiento a su escala; aquí la clave ocupa casi nada en la fila del pago, y no vencer es más estricto.

## Referencias

- [Implementing Stripe-like Idempotency Keys in Postgres](https://brandur.org/idempotency-keys), de Brandur Leach.
- [Idempotent requests](https://docs.stripe.com/api/idempotent_requests), en la documentación de Stripe.
- [Idempotencia en AWS Lambda Powertools](https://docs.aws.amazon.com/powertools/python/2.9.0/api/utilities/idempotency/persistence/dynamodb.html): la escritura condicional en DynamoDB.
- [The Idempotency-Key HTTP Header Field](https://datatracker.ietf.org/doc/draft-ietf-httpapi-idempotency-key-header/), el borrador de la IETF.
