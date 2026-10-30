# payment-processing-service

A payment processing service for online merchants. It authorizes card payments with an acquirer inside the merchant's request and answers with the result. When the acquirer gives no clear answer, it finishes the payment in the background, and tells the merchant by webhook through Kafka. It is designed so that no failure can charge a customer twice or lose an accepted payment.

> **Status:** the first iteration is complete. It has the design, the foundation (local environment, CI and shared platform packages), the acquirer simulator, merchant authentication, payments with their queries, their authorization in the request, the events relayed to Kafka, the recovery, which finishes or reverses the payments whose answer was not clear, the signed webhooks with their retries, the web app that plays the merchant (a checkout and a panel), the metrics, traces and alerts, and the [proof](#proof) under load and faults.

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
| Web app: checkout and panel | [localhost:3000](http://localhost:3000) |
| Payments API | `localhost:8080` |
| Acquirer simulator | `localhost:8090`, and `localhost:8091` for its book and faults |
| Grafana: the dashboard, traces and alerts | [localhost:3001](http://localhost:3001) |
| Prometheus | [localhost:9090](http://localhost:9090) |
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

### The web app

[localhost:3000](http://localhost:3000) plays Tienda Uno SpA, the demo merchant. Its server holds the merchant's secret key. The card is typed into the acquirer's own fields, in an iframe, so the merchant's page and server only ever see a token.

- **Pago de prueba** (`/checkout`): pays an order with a card, typed or chosen among the test cards inside the iframe, and shows the payment. When the answer is `202`, the page follows it until it is final. Sending the form twice gets the same payment back: the form keeps its idempotency key, and the iframe its token.
- **Pagos** (`/`): the merchant's payments, with filters by status and order, and each payment's history: its attempts, their outcome and latency, and its reversal.
- **Webhooks** (`/webhooks`): what the endpoint received, with the signature verified, and a switch that makes the endpoint answer `500`, to watch the retries.
- **Adquirente** (`/acquirer`): the simulator's faults (latency, errors, late answers, down) and its book.

The interface is in Spanish, as the merchants it plays are in Chile.

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

Compose points the local endpoints at the web app, which checks each signature and lists what it received at [localhost:3000/webhooks](http://localhost:3000/webhooks). The secrets are for local development only.

| Merchant | Webhook secret |
|---|---|
| Tienda Uno SpA | `whsec_local_only_tienda_uno` |
| Tienda Dos Limitada | `whsec_local_only_tienda_dos` |
| Comercio Suspendido SpA | `whsec_local_only_suspendida` |

```bash
docker compose exec postgres psql -U webhooks -d webhooks \
  -c 'SELECT event_type, status, attempts, last_error, next_attempt_at FROM deliveries'
```

### Observability

[localhost:3001](http://localhost:3001) opens Grafana on the **Payments** dashboard, with no login: payments per second and their outcomes, the latency of the request and of the acquirer's calls, the breaker, the recovery, the outbox and the webhooks, the service graph, and the alerts firing. A point on a latency graph opens the trace of that request in Tempo, from the `POST` to its webhook. The services push their traces and metrics over OTLP ([ADR 0006](docs/decisions/0006-observability-with-opentelemetry-prometheus-tempo-and-grafana.md)).

## Checks

```bash
make test              # unit tests, no Docker needed
make test-integration  # every test, with Postgres in a container
make web               # the web app's tests and build, with its type check
cd web && npm run e2e  # the browser tests, against `docker compose up` (PW_CHANNEL=chrome uses your Chrome)
make ci                # formatting, linters, every test, govulncheck and the web app, as in CI
make proof             # the load test with faults, then the consistency checker, against `docker compose up` (about 20 minutes)
```

## Proof

`make proof` runs [tests/load/run.sh](tests/load/run.sh) against the local environment in three phases: a nominal load, a load with faults, and the consistency checker. The output of the run reported here, and the dashboard during it, are in [docs/proof](docs/proof/).

**The machine:** a Mac mini M4, with 10 CPUs and 7.7 GB for Docker Desktop. The whole stack runs on it, k6 included, and every trace is kept. These numbers show what the design does on one machine. They are not a capacity plan for production.

### Nominal load

The simulator answers every authorization in 300 ms. k6 tokenizes a card at the simulator and pays with it through the API, at a constant rate for a minute. It starts each payment on time, whether the earlier ones have answered or not.

| Rate | Payments | p50 | p95 | p99 | Max |
|---|---|---|---|---|---|
| 100/s | 6,001 | 302.3 ms | 303.6 ms | 310.4 ms | 382.8 ms |
| 200/s | 12,000 | 301.8 ms | 302.8 ms | 304.2 ms | 334.2 ms |
| 300/s | 18,000 | 301.7 ms | 302.8 ms | 304.7 ms | 334.6 ms |
| 500/s | 30,001 | 302.0 ms | 303.6 ms | 311.1 ms | 358.6 ms |

Every payment was answered `201 APPROVED`, and k6 dropped none. At 500 per second, the service adds 11 ms in p99 to the acquirer's 300 ms, and NFR01 allows 50. That meets NFR02, 500 per second on one machine, with a margin that is not wide: in the run before this one, the p99 at 500 per second was 356.9 ms ([previous-run](docs/proof/previous-run/)). For about 20 s, requests waited for the API's database connections, while the acquirer's calls stayed at 310 ms.

![The Payments dashboard during the nominal load, from 100 to 500 payments per second](docs/proof/nominal.png)

### Faults

k6 pays 50 times per second for 5 minutes, with a mix of the test cards: most approve, and the rest decline, fail once, answer late, never answer or are rejected. Meanwhile, the script breaks things:

| At | What breaks | What the service does |
|---|---|---|
| 0:30 | The acquirer answers `503` to a third of its calls | Those payments answer `202`, and the recovery repeats their calls |
| 1:10 | It answers a fifth of its calls after the 5 s timeout | The request answers `202` at 5 s, and the next attempt gets the answer the acquirer stored for the key |
| 1:50 | It goes down for 30 s, tokenization too | No new card can be tokenized. The calls in flight are refused, both breakers open, and the recovery holds its attempts until the acquirer is back |
| 2:35 | Kafka stops for 45 s | Payments are still authorized and answered. Their events wait in the outbox, up to about 2,300, and drain when Kafka is back |
| 3:40 | `payments-api` is killed, and restarted | The requests in flight are cut. Their payments stay `PROCESSING`, and the recovery finishes them |
| 4:00 | `payments-worker` is killed, and restarted | Its leases expire, and the new worker claims those payments again |

What happened to the 15,001 payments k6 started:

- 1,502 never reached the API: their cards could not be tokenized while the acquirer was down.
- 32 found the API down while it restarted.
- The API answered the other 13,467. In its metrics, which lose the last seconds before the kill, there are about 9,479 `201`, 2,107 `202` and 1,788 `503`. The `503` came from the breaker, which opened during the `503` errors and while the acquirer was down.
- It created 11,753 payments. 9,367 were approved, 1,478 of them by the recovery, on their second or third attempt. 1,173 were declined, and 587 rejected by the acquirer. 626 ended `FAILED`, `acquirer_unavailable`, each after 3 attempts and a confirmed reversal: most are the card that never answers.

A payment's p50 was 53 ms; the acquirer answered in 50 ms in this phase. The p95 and the p99 were 5 s, the timeout of the calls that never got an answer. NFR01 allows the timeout plus 50 ms, 5.05 s. In the API's histogram, 13,374 of 13,375 requests took at most that, and one went past it by less than 50 ms.

![The Payments dashboard during the faults: the breaker, the recovery, the outbox filling while Kafka is down, and every payment finished at the end](docs/proof/faults.png)

### Consistency

Once the load ends, [the checker](tests/consistency/) waits until nothing moves: no payment `PROCESSING`, no event in the outbox, no webhook pending. Then it reads the payments database, the simulator's book and the deliveries of the webhooks service, and checks every invariant of the PRD over every payment of the run, nominal and with faults:

```
79256 payments (76870 APPROVED, 1173 DECLINED, 1213 FAILED), 78669 keys in the acquirer's book, 79256 webhook events. Settled after 27s.

Invariant  Rule                                      Checked  Violations
INV01      one key, one payment                      79256    0
INV02      one live or approved payment per order    79256    0
INV03      money is held only for approved payments  156126   0
INV04      every payment reaches a final state       79256    0
INV05      webhooks match the database and the book  79256    0
INV06      every final state is delivered once       79256    0

All invariants hold.
```

### What the proof found

**Money held for a failed payment.** The first full run did not pass. 42 of its 15,014 payments had ended `FAILED`, `acquirer_unavailable`, while the acquirer held a live authorization for them: money held on a customer's card for a payment the merchant was told had failed (INV03).

The cause was in Go's HTTP transport. When a kept-alive connection fails, the transport sends a request that carries an `Idempotency-Key` again, on a new connection, and returns the error of that second try. A call whose connection broke just as the simulator went down came back as "connection refused". The client read that as "certainly not sent", so the recovery did not reverse the payment. But the first try had arrived, and had been approved.

Now a refusal counts as not sent only when the request never had a connection. Otherwise it is uncertain, and the payment goes through the usual retries and the reversal. A test reproduces the sequence: it failed before the fix, and passes with it ([RFC, section 5.3](docs/rfc/0001-architecture.md#53-retries-uncertainty-and-reversal)). No unit, integration, acceptance or browser test had found it: it takes a connection reused at the moment the acquirer goes down, which only a load with faults produces.

**A recovery stopped by its own breaker.** The run before this one passed the checker, but the dashboard showed the recovery's breaker open for most of the faults, and opening before any fault, from the mix of cards alone. The recovery only calls for payments whose last attempt had no definite answer, so its calls fail in a row far more often than the API's. The card that never answers, 1 in 20 payments, was enough to keep its breaker open. After each cool-down, its probe was likely to be another such payment, which opened it again. Meanwhile the attempts of every other payment waited until their 2-minute window ended, and were reversed. Customers were not charged, but sales that a second attempt would have approved were lost.

The recovery's breaker now opens on refused connections only: a timeout on a retry is often about the payment, and a refused connection is about the acquirer ([RFC, section 5.4](docs/rfc/0001-architecture.md#54-circuit-breaker-and-load-shedding)). The same faults, before and after:

| Payments created during the faults | Before | After |
|---|---|---|
| Created | 11,723 | 11,753 |
| Approved | 8,052 | 9,367 |
| `FAILED`, `acquirer_unavailable` | 1,981 | 626 |
| Of those, with a single attempt | 1,896 | 0 |

### What the load found, and what changed

The first measurements were far from these numbers: from 50 per second on, the p99 of a payment was between 1 and 4 s. The traces, the pool metrics and the CPU of each container pointed to four causes, all fixed in this step:

1. **The relay ran in a tight loop.** Whenever it found rows, it read the outbox again at once, so under load it sent one query and one request to Kafka per event. It now waits after a round that does not fill its batch, and the events of the next moment travel together.
2. **The HTTP clients opened a connection for most calls.** Go's default transport keeps 2 idle connections per host. The acquirer's client now keeps up to 512, the API's limit of payments in flight, and the webhook sender 100.
3. **The API ran out of database connections.** Each payment holds one for T1 and another for T2, and requests waited for the pool of 20. It is now 50, and every service reports its pool: the connections in use and idle, and the waits.
4. **T1 and T2 made too many round trips to Postgres**: 4 and 6. T1 is now a single statement, and T2 a single pgx batch. In an A/B test on the same environment, at 100 per second, the p50 went from 1.98 s to 0.86 s.

Two errors were in the measuring itself. `docker compose run k6` recreated the services k6 depends on, which reset the simulator's latency and measured cold services; k6 now runs with `--no-deps`. And the histograms had buckets too coarse around 300 ms and 5 s, which moved the percentiles in Grafana; they are finer now. The percentiles in the tables above come from k6, which computes them from every request, not from buckets.

### What this proof does not show

- **Production.** One machine runs the service, its databases, Kafka, the simulator, the observability stack and k6. Each rate runs for a minute.
- **Stable numbers.** Runs on Docker Desktop vary. Besides the 500 per second above, one run on a fresh environment, before the two fixes above, had a p99 of 1.8 s at 100 per second, and the cause was not found. Another counted 60 failed requests at 200 per second (0.24 %) that the API never recorded; repeated, it had none. Every run reported here starts from `docker compose down --volumes`.
- **A real acquirer.** The simulator answers every call in exactly 300 ms; a real acquirer has a long tail.
- **The API's breaker at volume.** It opens after 5 failures in a row, so with a third of the calls failing, at 50 per second, it opened by chance and refused payments that the acquirer would have approved two times in three. A breaker on the failure rate over a window would not; it is in the [roadmap](docs/roadmap.md).

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
| 8. Checkout and panel | Done |
| 9. Proof | Done |

What each step delivers is in the [RFC](docs/rfc/0001-architecture.md#17-plan).

## Status labels

Every capability in this repository is marked as one of:

- **Designed:** described in the PRD or the RFC, not built yet.
- **Implemented:** built and covered by automated tests.
- **Proven:** demonstrated under load or failure, with the results in this repository.

Today, payments with their idempotency and their orders, their authorization in the request, the breaker, the recovery, the relay and the webhooks are proven, by the load with faults and the consistency checker of the [proof](#proof). The foundation, the acquirer simulator, merchant authentication, the web app, and the metrics, traces and alerts are implemented. So is the limit of 512 payments in flight, which no run reached. CI also runs the failures of the recovery against the local environment: the API killed during its call, Kafka down, and an acquirer that never answers. And it walks the web app in a browser: a payment made and followed, a card number that never reaches the merchant, the webhooks with their retries, and the acquirer down.
