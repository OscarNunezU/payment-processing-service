---
title: "ADR 0005: Procesamiento asíncrono con Postgres como cola"
status: accepted
date: 2026-10-15
deciders: oscar
---

# ADR 0005: Procesamiento asíncrono con Postgres como cola

## Contexto

Cada pago se autoriza con un adquirente externo que puede ser lento o responder con errores temporales. Si el pago se procesa dentro del request, la conexión del comercio queda abierta durante la llamada y sus reintentos. Si el comercio corta por timeout, no sabe si se cobró, y si el servicio se cae a mitad de camino, el pago queda sin terminar.

## Decisión

- `POST /payments` valida la entrada y las reglas de negocio, guarda la transacción como `PENDING` y responde `202 Accepted` con su id.
- Un worker (goroutines del mismo proceso) toma los pagos pendientes con `SELECT ... FOR UPDATE SKIP LOCKED`, los pasa a `PROCESSING`, llama al adquirente y guarda el estado final.
- La tabla de transacciones es la cola: guardar el pago y encolarlo es una sola escritura.
- Cada pago tomado tiene un plazo (lease). Si el worker muere, el plazo vence y otro worker lo retoma.
- Cada llamada al adquirente lleva el id de la transacción, para que reenviarla no autorice dos veces.
- El comercio consulta el estado con `GET /payments/{id}`.
- Los datos de tarjeta se guardan cifrados hasta que el worker los usa, y se borran al llegar a un estado final.

## Consecuencias

**Positivas**
- El POST responde en milisegundos aunque el adquirente esté lento.
- Los reintentos ocurren en segundo plano, sin conexiones abiertas.
- Un pago no se pierde si el servicio se cae: sigue en la tabla y se retoma.
- Varias réplicas comparten la cola sin tomar el mismo pago.

**Negativas**
- El comercio tiene que consultar el estado; no se le notifica.
- Hay más piezas: el worker, el lease y el apagado ordenado del worker.
- Los datos de tarjeta deben guardarse, cifrados, entre el POST y el procesamiento.

## Alternativas consideradas

- **Síncrono** (el POST espera al adquirente): es más simple, pero un adquirente lento o inestable retiene las conexiones, y un corte deja pagos en duda.
- **Cola en memoria**: si el proceso se cae, los pagos encolados se pierden.
- **Redis o RabbitMQ**: suman infraestructura y traen el problema de la doble escritura (guardar en Postgres y publicar en la cola), que obligaría a usar un outbox.

## Evolución

- Webhooks para notificar el estado final al comercio.
- Escalar los workers según el largo de la cola.
