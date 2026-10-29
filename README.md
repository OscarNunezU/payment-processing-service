# payment-processing-service

A payment processing service for online merchants. It accepts card payments in milliseconds, authorizes them with an acquirer in the background through Kafka, and tells the merchant the result by webhook. It is designed so that no failure can charge a customer twice or lose an accepted payment.

> **Status:** in progress. So far: the design, the foundation (local environment, CI and shared platform packages) and the acquirer simulator.

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
| Acquirer simulator | `localhost:8090`, and `localhost:8091` for its book and faults |
| Jaeger (traces) | [localhost:16686](http://localhost:16686) |
| Postgres | `localhost:5432` (one database and one role per service) |
| Kafka | `localhost:9094` |

`docker compose down --volumes` stops everything and deletes its data.

### The acquirer simulator

The simulator tokenizes cards and authorizes them by idempotency key, as the acquirer would. Its test cards and its faults are in the [RFC](docs/rfc/0001-architecture.md#10-acquirer-simulator). The keys below are for local development only.

```bash
token=$(curl -s localhost:8090/v1/tokens -H 'Authorization: Bearer pk_sim_local_only' \
  -d '{"number":"4111111111111111","exp_month":12,"exp_year":2035,"cvc":"123"}' | jq -r .token)
curl -s localhost:8090/v1/authorizations -H 'Authorization: Bearer sk_sim_local_only' \
  -H 'Idempotency-Key: demo-1' -d "{\"token\":\"$token\",\"amount\":15000,\"currency\":\"CLP\"}"
curl -s localhost:8091/admin/book                                         # what the acquirer holds
curl -s -X PUT localhost:8091/admin/faults -d '{"down":true}'             # refuse connections
curl -s -X PUT localhost:8091/admin/faults -d '{}'                        # back to normal
```

While the simulator is down, the services inside Compose get "connection refused". From the host, Docker's port forwarding accepts the connection first, so the same fault shows up as a connection reset.

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
| 1. Scope | Done |
| 2. Acquirer simulator | In progress |
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

Today, the foundation and the acquirer simulator are implemented; everything else is designed.
