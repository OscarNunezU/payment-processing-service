---
title: "ADR 0008: Trazabilidad con una tabla de eventos"
status: accepted
date: 2026-10-15
deciders: oscar
---

# ADR 0008: Trazabilidad con una tabla de eventos

## Contexto

El enunciado pide diseñar el modelo de datos considerando la trazabilidad de las operaciones y permitir correlacionar los eventos y acciones de una misma transacción. La tabla `transactions` guarda solo el estado actual: cuando un pago termina `APPROVED`, ya no se sabe cuándo lo tomó el worker ni en qué intento.

## Decisión

- Una tabla `transaction_events` a la que solo se le agregan filas: una por cada cambio del pago. `transactions` sigue siendo el estado actual, que es lo que se consulta seguido.
- Cada evento guarda el estado en que quedó el pago, el intento, el motivo y la fecha, sin un tipo de evento aparte: el estado y el motivo alcanzan para contar la historia. Un cambio que no mueve el estado, como reprogramar un intento, se registraría con el mismo estado y su motivo.
- El evento se escribe en la misma sentencia SQL que el cambio, con un `WITH` (CTE): `WITH changed AS (UPDATE … RETURNING *) INSERT INTO transaction_events SELECT … FROM changed`. Una sentencia es atómica: no puede quedar un cambio sin su evento, ni un evento sin su cambio, y sigue siendo un solo viaje a la base. Si el cambio no ocurre (un replay idempotente o un `Finish` rechazado), no hay evento.
- Se registran la creación (`PENDING`), cada toma del worker (`PROCESSING` con su número de intento) y el resultado final con su motivo. Una toma repetida porque venció el lease se ve como un segundo `PROCESSING` sin resultado entre medio.
- El id de la transacción es el id de correlación. Está en sus eventos, en los logs del worker, en los del adquirente simulado (le llega como `Idempotency-Key`) y en los logs de la API que crean o devuelven el pago (`payment accepted` y `payment replayed`), que llevan además el `request_id` que los une con el log de acceso.
- `GET /payments/{id}/events` devuelve la historia, de lo más antiguo a lo más reciente, o `404` si el pago no existe. Es un recurso aparte para que el `GET` del pago, el que el comercio consulta una y otra vez, siga siendo liviano.
- Los replays idempotentes no son eventos, porque no cambian el pago: quedan en los logs como `payment replayed`.
- Los pagos creados antes de esta tabla no tienen historia. Se sabe cuándo se crearon y su último cambio, pero no cuándo los tomó el worker, y una historia a medias engañaría. En una base nueva no existen.

## Consecuencias

**Positivas**
- La historia de un pago se consulta con una petición, y la de todos con SQL simple (por ejemplo, los pagos que necesitaron más de un intento).
- El cambio y su evento son atómicos sin abrir una transacción explícita.
- Los logs de la API, del worker y del adquirente, y la historia en la base, se unen por el mismo id.

**Negativas**
- Cada cambio escribe una fila más. En un A/B intercalado de 6 rondas contra la versión sin eventos, crear rindió un 6 % menos (~8.300 contra ~8.850 req/s, y +0,4 ms en la mediana de latencia) y el worker un 3 % menos (~1.260 contra ~1.300 pagos/s), porque en él pesa más la llamada al adquirente.
- La tabla crece unas 3 filas por pago. En producción habría que particionarla por fecha o archivar la historia antigua.
- Una fila `PROCESSING` sin motivo es una toma del worker. Si aparecen eventos que no calzan con estado y motivo (por ejemplo, acciones manuales de soporte), habrá que agregar una columna de tipo.

## Alternativas consideradas

- **Transacción explícita con dos sentencias**: también es atómica, pero son 4 viajes a la base (`BEGIN`, el cambio, el evento y `COMMIT`) en vez de 1.
- **Trigger en la base**: escribiría el evento solo, pero deja la lógica repartida entre Go y SQL, sin que se vea al leer el repositorio. Además, el motivo de un intento reprogramado no viviría en la fila del pago, así que el trigger no tendría de dónde sacarlo.
- **Solo logs**: no se consultan desde la API y se rotan.
- **Columna JSON con la historia en la fila del pago**: la historia crece dentro de la fila, se reescribe entera en cada cambio y no se puede consultar con SQL simple.
- **Event sourcing** (el estado se reconstruye desde los eventos): es más de lo que este alcance necesita. Aquí la fila del pago es el estado y los eventos, el registro.
- **Guardar el `request_id` en el evento de creación**: acoplaría el repositorio al contexto HTTP, y el log `payment accepted` ya une los dos ids.
