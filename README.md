# payment-processing-service

A payments platform for merchants: it authorizes and captures card payments through two acquirers, records every money movement in a double-entry ledger, reconciles the acquirers' settlements and pays each merchant. It is designed so that no failure can lose or duplicate money without being detected.

> **Status:** milestone 1 (reliable core) is in progress. So far: the foundation, with the local environment, CI and shared platform packages.

## Documents

- [PRD](docs/prd.md): what the platform does and why.
- [Architecture RFC](docs/rfc/0001-architecture.md): how it is built, and the plan for milestone 1.
- [Decisions](docs/decisions/): one ADR per significant decision.
- [TLA+ model](spec/authorization/Authorization.tla) of the authorization protocol, checked by TLC in CI.

## Run it locally

Requirements: Docker. For development, Go as well; the toolchain downloads Go 1.27.3 by itself.

```bash
docker compose up --build --wait   # starts everything and waits until it is healthy
curl -s localhost:8080/readyz      # {"status":"ready","checks":{"postgres":"ok","valkey-risk":"ok"}}
```

| Service | Address |
|---|---|
| Payments API | `localhost:8080` |
| Jaeger (traces) | [localhost:16686](http://localhost:16686) |
| Postgres | `localhost:5432` (one database and one role per service) |
| Kafka | `localhost:9094` |
| Valkey, risk and vault | `localhost:6379`, `localhost:6380` |

`docker compose down --volumes` stops everything and deletes its data.

## Checks

```bash
make test              # unit tests, no Docker needed
make test-integration  # every test, with Postgres and Valkey in containers
make ci                # formatting, linters, every test and govulncheck, as in CI
make spec              # the TLA+ model of the authorization protocol, and its mutations (needs Java)
```

## Milestones

| Milestone | Status |
|---|---|
| 1. Reliable core | In progress |
| 2. Money adds up | Not started |
| 3. Merchant and operations | Not started |
| 4. Production | Not started |

## Status labels

Every capability in this repository is marked as one of:

- **Designed:** described in the PRD or the RFC, not built yet.
- **Implemented:** built and covered by automated tests.
- **Proven:** demonstrated under load or failure, with the results in this repository.

Today, the foundation is implemented; everything else is designed.
