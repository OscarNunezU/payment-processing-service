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
  -d '{"merchant_id":"merchant-001","amount":15000,"currency":"CLP",
       "card":{"number":"4111111111111111","expiry_month":12,"expiry_year":2030,"cvv":"123"}}'
```

Un worker toma el pago en segundo plano, lo autoriza con el adquirente simulado y lo deja en su estado final, normalmente en menos de un segundo. Para ver el resultado, consulta el pago, o lista los pagos con filtros (`merchant_id`, `status` y `limit` son opcionales; por defecto 50, máximo 100):

```bash
curl -s localhost:8080/payments/<transaction_id>
curl -s 'localhost:8080/payments?merchant_id=merchant-001&status=APPROVED&limit=10'
```

### Tarjetas de prueba

El adquirente simulado decide según el número de tarjeta:

| Tarjeta | Resultado |
|---|---|
| `4111111111111111` | `APPROVED`, con `authorization_code` |
| `4000000000000002` | `DECLINED`, motivo `card_declined` |
| `4000000000009995` | `DECLINED`, motivo `insufficient_funds` |

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
