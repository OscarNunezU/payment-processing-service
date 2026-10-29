# payment-processing-service

A payment processing service for online merchants. It authorizes card payments with an acquirer inside the merchant's request and answers with the result. When the acquirer gives no clear answer, it finishes the payment in the background, and tells the merchant by webhook through Kafka. It is designed so that no failure can charge a customer twice or lose an accepted payment.

> **Status:** in progress. So far: the design, the foundation (local environment, CI and shared platform packages), the acquirer simulator, merchant authentication, payments with their queries, their authorization in the request, the events relayed to Kafka, the recovery, which finishes or reverses the payments whose answer was not clear, and the signed webhooks with their retries. The web app that receives the webhooks locally arrives in step 8.

## Documents

- [PRD](docs/prd.md): what the service does and why.
- [Architecture RFC](docs/rfc/0001-architecture.md): how it is built, and the plan.
- [Roadmap](docs/roadmap.md): what could come after this iteration.
- [Decisions](docs/decisions/): one ADR per significant decision.

## Run it locally

Requirements: Docker. For development, Go as well; the toolchain downloads Go 1.27.3 by itself.

```bash
docker compose up --build --wait   # starts everything and waits until it is healthy
curl -s localhost:8080/readyz      # {"status":"ready","checks":{"acquirer":"ok","postgres":"ok"}}
```

| Service | Address |
|---|---|
| Payments API | `localhost:8080` |
| Acquirer simulator | `localhost:8090`, and `localhost:8091` for its book and faults |
| Jaeger (traces) | [localhost:16686](http://localhost:16686) |
| Postgres | `localhost:5432` (one database and one role per service) |
| Kafka | `localhost:9094` |

`docker compose down --volumes` stops everything and deletes its data.

### Local merchants

Compose loads three merchants. Their keys are for local development only.

| Merchant | Status | API key |
|---|---|---|
| Tienda Uno SpA (76.543.210-3) | Active | `sk_test_local_tienda_uno` |
| Tienda Dos Limitada (77.123.456-9) | Active | `sk_test_local_tienda_dos` |
| Comercio Suspendido SpA (76.987.654-5) | Suspended | `sk_test_local_suspendida` |

```bash
curl -s localhost:8080/v1/merchant -H 'Authorization: Bearer sk_test_local_tienda_uno'
```

### Payments

```bash
token=$(curl -s localhost:8090/v1/tokens -H 'Authorization: Bearer pk_sim_local_only' \
  -d '{"number":"4111111111111111","exp_month":12,"exp_year":2035,"cvc":"123"}' | jq -r .token)
curl -si localhost:8080/v1/payments -H 'Authorization: Bearer sk_test_local_tienda_uno' \
  -H 'Idempotency-Key: order-1001-1' \
  -d "{\"order_id\":\"1001\",\"amount\":15000,\"currency\":\"CLP\",\"card_token\":\"$token\"}"
curl -s localhost:8080/v1/payments/<id> -H 'Authorization: Bearer sk_test_local_tienda_uno'
curl -s 'localhost:8080/v1/payments?status=APPROVED&limit=10' -H 'Authorization: Bearer sk_test_local_tienda_uno'
```

The answer is `201` with the result, such as `APPROVED` and its authorization code. When the acquirer gives no clear answer, it is `202` with the payment `PROCESSING`, and `payments-worker` finishes it in the background. Sending the same request again with the same `Idempotency-Key` returns the same payment, with `Idempotent-Replayed: true`, without calling the acquirer again.

Some test cards show the recovery at work, and the payment's history shows each call:

- `4000000000000127` fails the first attempt: the answer is `202`, and the payment is `APPROVED` a couple of seconds later, on attempt 2.
- `4000000000000135` approves after the 5 s timeout: the answer is `202`, and the next attempt gets the approval the acquirer stored for the key.
- `4000000000000150` never answers: after 3 attempts the authorization is reversed, and the payment ends `FAILED` with reason `acquirer_unavailable`, about 30 s after the request.

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

### Webhooks

Every final payment produces one webhook to its merchant's endpoint: a `POST` with the event as its body, its id in `Webhook-Id`, and `Webhook-Signature: t=<unix seconds>,v1=<hex HMAC-SHA256 of "t.body">`. A receiver checks the signature with its secret, rejects timestamps more than 5 minutes away, and discards an id it already processed. A webhook that is not answered with a `2xx` is sent again, from 10 s up to every hour, for 24 hours.

Compose points the local endpoints at the web app, which arrives in step 8; until then their attempts fail and are retried. The secrets are for local development only.

| Merchant | Webhook secret |
|---|---|
| Tienda Uno SpA | `whsec_local_only_tienda_uno` |
| Tienda Dos Limitada | `whsec_local_only_tienda_dos` |
| Comercio Suspendido SpA | `whsec_local_only_suspendida` |

```bash
docker compose exec postgres psql -U webhooks -d webhooks \
  -c 'SELECT event_type, status, attempts, last_error, next_attempt_at FROM deliveries'
```

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
| 2. Acquirer simulator | Done |
| 3. Merchants | Done |
| 4. Accepting and querying payments | Done |
| 5. Authorization in the request | Done |
| 6. Recovery | Done |
| 7. Webhooks | Done |
| 8. Checkout and panel | Not started |
| 9. Proof | Not started |

What each step delivers is in the [RFC](docs/rfc/0001-architecture.md#17-plan).

## Status labels

Every capability in this repository is marked as one of:

- **Designed:** described in the PRD or the RFC, not built yet.
- **Implemented:** built and covered by automated tests.
- **Proven:** demonstrated under load or failure, with the results in this repository.

Today, the foundation, the acquirer simulator, merchant authentication, payments, their authorization in the request, the relay, the recovery and the webhooks are implemented; everything else is designed. CI also runs the failures of the recovery against the local environment: the API killed during its call, Kafka down, and an acquirer that never answers.
