# Payment Processing Service

Servicio que recibe solicitudes de pago de comercios, las autoriza con un adquirente y deja trazabilidad de cada transacción.

> En construcción.

## Cómo correrlo

Requisito: Docker.

```bash
docker compose up --build
curl -i localhost:8080/health   # 200 {"status":"ok"}
```

Crear un pago (responde `202` con la transacción en `PENDING` y el header `Location`):

```bash
curl -i -X POST localhost:8080/payments \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: order-1001' \
  -d '{"merchant_id":"merchant-001","amount":15000,"currency":"CLP",
       "card":{"number":"4111111111111111","expiry_month":12,"expiry_year":2030,"cvv":"123"}}'
```

Un worker toma el pago en segundo plano, lo autoriza con el adquirente simulado y lo deja en su estado final, normalmente en menos de un segundo. Para ver el resultado, consulta el pago, o lista los pagos con filtros (`merchant_id`, `status` y `limit` son opcionales; por defecto 50, máximo 100):

```bash
curl -s localhost:8080/payments/<transaction_id>
curl -s 'localhost:8080/payments?merchant_id=merchant-001&status=APPROVED&limit=10'
```

Cada cambio del pago queda en su historia, con el intento y el motivo:

```bash
curl -s localhost:8080/payments/<transaction_id>/events
```

### Idempotencia

Cada `POST /payments` lleva el header `Idempotency-Key`, elegido por el comercio (por ejemplo, el id de su orden). Una clave identifica un solo pago por comercio:

| Request | Respuesta |
|---|---|
| clave nueva | `202`: se crea el pago |
| la misma clave y el mismo pago (un reintento) | `202` con el mismo pago en su estado actual y el header `Idempotent-Replayed: true` |
| la misma clave con otro pago | `422 idempotency_key_reused` |
| sin clave | `400` |

Reintentar con la misma clave devuelve el mismo resultado, aunque sea `FAILED`; para un intento nuevo se usa una clave nueva. Una request rechazada (`400` o `422`) no guarda nada, así que se puede corregir y reenviar con la misma clave. Detalle en la [ADR 0007](docs/decisions/0007-idempotencia-por-comercio.md).

### Tarjetas de prueba

El adquirente simulado decide según el número de tarjeta:

| Tarjeta | Resultado |
|---|---|
| `4111111111111111` | `APPROVED`, con `authorization_code` |
| `4000000000000002` | `DECLINED`, motivo `card_declined` |
| `4000000000009995` | `DECLINED`, motivo `insufficient_funds` |
| `4000000000000119` | el adquirente responde `503` siempre: 3 intentos y `FAILED`, motivo `acquirer_unavailable` |
| `4000000000000127` | `503` la primera vez: el segundo intento termina `APPROVED` |
| `4000000000000135` | aprueba, pero responde después del timeout: el reintento recibe la misma aprobación |
| `4000000000000143` | el adquirente rechaza la request (`422`): `FAILED`, motivo `acquirer_rejected`, sin reintentos |

### Errores temporales del adquirente

Ante un timeout, un `5xx`, un `408` o `429`, una conexión caída o una respuesta ilegible, el pago tiene hasta 3 intentos, con esperas de 1-2 s y luego 2-4 s entre ellos. Reintentar es seguro porque cada llamada lleva el id de la transacción como `Idempotency-Key`, y cada intento queda en la historia del pago. Detalle en la [ADR 0009](docs/decisions/0009-reintentos-de-errores-temporales.md).

Si el adquirente acumula 5 fallas seguidas, un circuit breaker deja de llamarlo por 30 s: el worker no toma pagos, y estos esperan en la cola sin gastar intentos. Un pago que sigue `PENDING` después de 2 minutos, sin haberse enviado nunca, termina `FAILED` con motivo `expired`. Detalle en la [ADR 0010](docs/decisions/0010-breaker-y-vencimiento.md).

### Desarrollo local sin Docker para la API

```bash
docker compose up -d postgres acquirer-mock
cp .env.example .env
# completa CARD_ENCRYPTION_KEY en .env con: openssl rand -base64 32
set -a && source .env && set +a
cd backend && go run ./cmd/server
```

## Tests

```bash
make ci                 # formato, vet y tests con -race; sin Docker (los de Postgres se saltan)
make test-integration   # todos los tests, incluidos los de Postgres (levanta el Postgres de compose)
```

El CI corre los dos: levanta un Postgres como servicio, así que ningún test se salta.

## Decisiones

| ADR | Decisión |
|---|---|
| [0001](docs/decisions/0001-net-http-sin-framework.md) | `net/http` de la librería estándar, sin framework |
| [0002](docs/decisions/0002-go-1.26.md) | Go 1.26.9, fijado en `go.mod` |
| [0003](docs/decisions/0003-arquitectura-hexagonal-compacta.md) | Arquitectura hexagonal compacta |
| [0004](docs/decisions/0004-montos-en-enteros.md) | Montos como enteros en la unidad mínima de la moneda |
| [0005](docs/decisions/0005-procesamiento-asincrono.md) | Procesamiento asíncrono con Postgres como cola |
| [0006](docs/decisions/0006-estrategia-de-tests.md) | Tests con la librería estándar y Postgres real |
| [0007](docs/decisions/0007-idempotencia-por-comercio.md) | Idempotencia por comercio con el header `Idempotency-Key` |
| [0008](docs/decisions/0008-trazabilidad-con-eventos.md) | Trazabilidad con una tabla de eventos escrita junto con cada cambio |
| [0009](docs/decisions/0009-reintentos-de-errores-temporales.md) | Reintentos de errores temporales del adquirente, con backoff y jitter |
| [0010](docs/decisions/0010-breaker-y-vencimiento.md) | Circuit breaker ante un adquirente caído y vencimiento de los pagos no enviados |
