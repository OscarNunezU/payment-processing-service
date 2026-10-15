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
make ci   # formato, vet y tests con -race: lo mismo que corre el CI
```
