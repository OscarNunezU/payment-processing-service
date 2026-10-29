# payment-processing-service

A payment processing service for online merchants. It accepts card payments in milliseconds, authorizes them with an acquirer in the background through Kafka, and tells the merchant the result by webhook. It is designed so that no failure can charge a customer twice or lose an accepted payment.

> **Status:** in progress. So far: the design and the foundation, with the local environment, CI and the shared platform packages.

## Documents

- [PRD](docs/prd.md): what the service does and why.
- [Architecture RFC](docs/rfc/0001-architecture.md): how it is built, and the plan.
- [Roadmap](docs/roadmap.md): what could come after this iteration.
- [Decisions](docs/decisions/): one ADR per significant decision.

## Run it locally

Requirements: Docker. For development, Go as well; the toolchain downloads Go 1.27.3 by itself.

```bash
docker compose up --build --wait   # starts everything and waits until it is healthy
curl -s localhost:8080/readyz      # {"status":"ready","checks":{"postgres":"ok"}}
```

| Service | Address |
|---|---|
| Payments API | `localhost:8080` |
| Jaeger (traces) | [localhost:16686](http://localhost:16686) |
| Postgres | `localhost:5432` (one database and one role per service) |
| Kafka | `localhost:9094` |

`docker compose down --volumes` stops everything and deletes its data.

## Checks

```bash
make test              # unit tests, no Docker needed
make test-integration  # every test, with Postgres in a container
make ci                # formatting, linters, every test and govulncheck, as in CI
```

## Plan

| Step | Status |
|---|---|
| Foundation | Done |
| 1. Scope | In progress |
| 2. Acquirer simulator | Not started |
| 3. Merchants | Not started |
| 4. Accepting and querying payments | Not started |
| 5. Relay and processing | Not started |
| 6. Uncertainty and failures | Not started |
| 7. Webhooks | Not started |
| 8. Checkout and panel | Not started |
| 9. Proof | Not started |

What each step delivers is in the [RFC](docs/rfc/0001-architecture.md#17-plan).

## Status labels

Every capability in this repository is marked as one of:

- **Designed:** described in the PRD or the RFC, not built yet.
- **Implemented:** built and covered by automated tests.
- **Proven:** demonstrated under load or failure, with the results in this repository.

Today, the foundation is implemented; everything else is designed.
