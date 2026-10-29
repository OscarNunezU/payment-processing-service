# ADR 0005: Authorize in the request, recover in the background

- **Status:** accepted
- **Date:** 2026-10-29

## Context

The first version of RFC 0001 authorized payments in the background. The `POST` saved the payment as `PENDING` and answered `202`. Kafka carried the work to a pool of workers, and the merchant learned the result by webhook or by polling.

That design keeps the API independent of the acquirer's latency, but it has two costs:

- **The merchant never gets the result in the response.** A checkout has to poll or wait for a webhook, even when the acquirer answers in 300 ms. Card APIs such as Stripe's or Adyen's return the authorization in the same call.
- **Kafka is on the payment path.** While Kafka is down, payments are accepted but not processed, and after 2 minutes they expire.

A synchronous API alone is not enough either. A request can be cut at the worst moment: after the acquirer approved and before the answer was recorded. Without a recovery, that payment stays stuck, or it is failed while the card was charged.

## Decision

- **The API authorizes inside the request.** In one transaction it records the payment as `PROCESSING` and the first attempt, with a lease. Then it calls the acquirer with the payment id as the key, and records the answer.
- **The answer depends on what the acquirer said:**
  - `201` with the final state when the acquirer answers;
  - `202` with `PROCESSING` when the answer is not clear;
  - `503` with `Retry-After`, creating nothing, when the circuit breaker is open or the instance is at its limit. The check runs inside T1, after the insert finds whether the key is new, so a retry of an existing payment always gets that payment.
- **A recovery worker finishes what the request could not.** It claims the payments whose lease expired from Postgres, with `FOR UPDATE SKIP LOCKED` and epochs, repeats the call with the same key, and reverses when the outcome stays unknown.
- **Kafka only carries the final events** to the webhooks service and future consumers. The payment path depends on Postgres and the acquirer only.
- **The `PENDING` status and the `expired` reason disappear.** A payment starts in `PROCESSING`. An authorization window of 2 minutes replaces the expiry, so nothing is authorized late.

## Consequences

- In the normal case, the merchant gets the result in the response, and a checkout needs no polling.
- If Kafka is down, payments are still authorized and answered; only the webhooks are delayed.
- The API holds each request for as long as the acquirer takes, up to its 5 s timeout. Each instance caps its payments in flight at 512 and sheds load beyond that with `503`.
- Processing gets simpler: there is no parallel Kafka consumer with ordered offset commits. The recovery only sees the few uncertain payments, and claims them from Postgres the way v1's worker did.
- The work already merged keeps its value. Validation, rules, idempotency, orders, isolation, the queries, the outbox and the event contracts stay; the `POST` flow and its status codes change in step 5.
- Migration 00003 created `PENDING` and `expired`. Since it is already applied, a new migration in step 5 removes them, as ADR 0003 requires.

## Alternatives considered

- **Keep the background authorization:** independence from the acquirer's latency, at the cost of the two points in the context.
- **Background authorization with a short wait in the `POST`:** the merchant would often get the result, but Kafka would stay on the payment path, with more moving parts between a request and its answer.
- **Authorization in the request without a recovery:** the simplest option, and the one that leaves payments stuck or failed blindly when a request is cut.
