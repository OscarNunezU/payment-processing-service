---
title: "ADR 0010: Circuit breaker y vencimiento de pagos no enviados"
status: accepted
date: 2026-10-15
deciders: oscar
---

# ADR 0010: Circuit breaker y vencimiento de pagos no enviados

## Contexto

Con los reintentos de la ADR 0009, una falla breve del adquirente ya no hace fallar los pagos. Una caída larga sí: cada pago gasta sus 3 intentos en unos segundos y termina `FAILED`, mientras los workers siguen llamando a un adquirente que no responde. Además, un pago no debería autorizarse mucho después de pedido. En la industria una autorización se resuelve en segundos, y si no llega respuesta, la red la anula con un reverso por timeout.

## Decisión

- **Circuit breaker, como decorador del cliente del adquirente.** Implementa la misma interfaz, así que el núcleo no se entera.
  - Tras 5 fallas temporales seguidas se abre por 30 s. Mientras está abierto, el worker deja de tomar pagos: esperan en la cola sin gastar intentos, y el adquirente no recibe más llamadas.
  - Una respuesta del adquirente (una aprobación, un rechazo o una request rechazada) reinicia la cuenta y cierra el circuito, aunque la llamada haya empezado antes de que se abriera.
  - Pasado el enfriamiento, los workers vuelven a tomar pagos, y una falla más lo reabre de inmediato.
  - La cola se retiene, en vez de fallar al tiro como en el breaker clásico, porque hay un solo adquirente: no hay a dónde desviar el tráfico, y fallar al tiro convertiría cada falso positivo en pagos `FAILED`. Reteniendo, un falso positivo cuesta una espera.
- **Vencimiento.** Cada 30 s, el worker pasa a `FAILED`, con motivo `expired`, los pagos que siguen `PENDING` después de 2 minutos. Un pago `PENDING` nunca se envió, así que vencerlo no deja nada abierto con el adquirente.
  - Los pagos que ya se enviaron no vencen, porque el adquirente pudo haberlos autorizado. A esos los acota el máximo de intentos.
  - Todas las réplicas corren el barrido, y es seguro: el barrido y la toma de un worker bloquean la fila y revisan su estado, así que un pago vence o se procesa, nunca las dos cosas.
- **Los valores (5 fallas, 30 s de enfriamiento y 2 minutos para vencer) son supuestos.** El criterio es que el vencimiento sea más largo que la peor cola esperada y más corto que lo que espera un cliente en el checkout o frente a un POS. 2 minutos dejan unos 4 ciclos del breaker para que el adquirente se recupere. Con datos reales del adquirente y del tráfico, se ajustan.

## Consecuencias

**Positivas**
- Una caída del adquirente ya no agota los intentos de los pagos: esperan en la cola y se procesan cuando vuelve.
- Un adquirente caído no recibe llamadas que no puede atender.
- Un pago que nunca se envió vence a los 2 minutos, en vez de autorizarse tarde.

**Negativas**
- El breaker es por proceso: cada réplica cuenta sus propias fallas.
- El umbral cuenta fallas seguidas de cualquier pago. Con poco tráfico, dos pagos con problemas pueden abrir el circuito para todos y hacer esperar hasta 30 s a pagos sanos; con tráfico real, las respuestas de otros pagos reinician la cuenta.
- Al terminar el enfriamiento no hay un único intento de prueba: con 4 workers, si la caída sigue, se pueden gastar hasta 4 intentos antes de que se reabra.
- Los pagos ya enviados que esperan un reintento no vencen. Durante una caída larga esperan junto con el circuito, y si el adquirente nunca los había recibido, se autorizan tarde.
- El vencimiento actúa también con carga, no solo con caídas. Con un adquirente real de unos 500 ms y 4 workers se procesan unos 960 pagos en 2 minutos, y un pico mayor vencería pagos aunque el adquirente esté sano. Por eso hay que dimensionar los workers para que la cola quede muy por debajo de 2 minutos y vigilar su largo, como prevé la evolución de la ADR 0005.
- Si la caída dura más de 2 minutos, los pagos que nunca se enviaron terminan `expired`, y el comercio debe reintentar con una clave nueva.

## Alternativas consideradas

- **Breaker dentro del núcleo**: mezcla la protección de la infraestructura con las reglas del pago. Como decorador, el núcleo no cambia.
- **Fallar de inmediato con el circuito abierto, como el breaker clásico**: con un solo adquirente, cada falso positivo terminaría en pagos `FAILED`, y cada pago tomado gastaría un intento sin llegar al adquirente.
- **Un único intento de prueba al terminar el enfriamiento (half-open estricto)**: ahorra intentos, pero obliga a coordinar a los workers. Con 4, lo que se pierde por no hacerlo es poco.
- **Un breaker compartido entre réplicas, en Postgres o Redis**: agrega estado compartido; por proceso alcanza para este alcance.
- **Un umbral por tasa de fallas en una ventana de tiempo**: evita los falsos positivos con poco tráfico, pero necesita un mínimo de llamadas y más estado. Para empezar, alcanza con contar fallas seguidas.
- **Vencer también los pagos ya enviados**: podrían estar autorizados en el adquirente, y vencerlos dejaría un cobro sin su pago.
- **Un vencimiento de 10 minutos**: tolera más carga, pero deja una ventana larga para autorizar tarde un pago que el comercio ya dio por perdido. Como la API no permite cancelarlo, el cliente que volvió a pagar con otra tarjeta podría terminar con dos cobros.
- **Una librería como sony/gobreaker**: un contador y un plazo caben en unas 40 líneas sin dependencias nuevas, en la línea de la ADR 0001.

## Evolución

- Un segundo adquirente con desvío. Ahí el breaker rinde de verdad: con el circuito abierto, el tráfico va al adquirente de respaldo, con la misma clave de idempotencia.
- Un reverso por timeout, para anular la autorización que pudo quedar abierta, como hacen las redes de tarjetas (ISO 8583) y Adyen con su cancelación técnica.
- Un endpoint para cancelar un pago que todavía no se envió.
- La conciliación diaria con los archivos del adquirente, para detectar pagos `FAILED` que en realidad se autorizaron.
