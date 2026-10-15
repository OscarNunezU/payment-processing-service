# Payment Processing Service

Servicio que recibe solicitudes de pago de comercios, las autoriza con un adquirente y deja trazabilidad de cada transacción.

> En construcción.

## Cómo correrlo

Requisito: Docker.

```bash
docker compose up --build
curl -i localhost:8080/health   # 200 {"status":"ok"}
```

### Desarrollo local sin Docker para la API

```bash
docker compose up -d postgres
cp .env.example .env
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
