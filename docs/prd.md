# PRD: Payment processing service

**Status:** draft for review · **Date:** 2026-10-28 · **Updated:** 2026-10-29, the result in the same response ([ADR 0005](decisions/0005-authorize-in-the-request-recover-in-the-background.md)) · **Owner:** Oscar Núñez

## 1. Summary

A service that lets online merchants in Chile accept card payments. A merchant sends a payment, and the service authorizes it with the acquirer and answers with the result in the same response. When the acquirer gives no clear answer, the service answers at once that the payment is processing, finishes it in the background and tells the merchant the result by webhook. Every payment keeps its full history.

The engineering goal: no failure may charge a customer twice, lose an accepted payment, or tell the merchant something that did not happen. That holds whether an instance crashes, the acquirer is slow or unavailable, or Kafka is down.

This is the first iteration: authorization only, with one acquirer. What comes next is in the [roadmap](roadmap.md).

## 2. Problem

A merchant's checkout needs to charge a card without the merchant handling card data, and with the certainty that:

- a customer is never charged twice for the same purchase, even if the merchant retries or the customer clicks twice;
- an accepted payment always reaches a final result, even if something fails along the way;
- the merchant can explain every payment: what was asked, what the acquirer answered and when.

Acquirers are slow at times and unavailable at others, and a timeout does not say whether the card was charged. The service has to absorb that without making the merchant wait more than a few seconds, and without guessing.

## 3. Goals

1. Answer with the result in the same response when the acquirer answers, and within the acquirer's timeout when it does not.
2. Zero double charges and zero lost payments, demonstrated with failure tests.
3. Horizontal scaling: more worker instances process more payments, up to the number of Kafka partitions.
4. The service never sees card numbers.
5. Every payment is traceable: its history in the database, and a single trace from the request to the webhook.

## 4. Non-goals in this iteration

- Capture, void and refunds: a payment is authorized or it is not.
- More than one acquirer, and routing between them.
- Ledger, settlement, reconciliation and payouts.
- Risk rules and fraud detection.
- Merchant onboarding, a merchant portal with login, and key rotation.
- Kubernetes, and SLOs with error budgets. Metrics, a dashboard and alerts are part of the proof (ADR 0006).
- 3-D Secure.

Each of them is in the [roadmap](roadmap.md), with what it would change.

## 5. Actors

| Actor | Role |
|---|---|
| Merchant | Its server creates and queries payments with a secret API key, and receives webhooks. Its checkout page runs in the cardholder's browser. |
| Cardholder | Enters the card in the merchant's checkout. |
| Acquirer | Simulated. Tokenizes cards from the browser, authorizes by idempotency key and reverses authorizations. Its latency and failures can be configured. |

## 6. Glossary

| Term | Meaning |
|---|---|
| Acquirer | The company that connects the service to the card networks and the issuers. |
| Authorization | The issuer approves the payment and holds the amount on the card. |
| Token | A reference to a card, created by the acquirer. It does not reveal the card number. |
| Reversal | Cancels an authorization whose outcome is unknown, for example after a timeout. After a reversal, the acquirer rejects any later authorization with the same key. |
| Idempotency key | A value the merchant chooses to identify one payment request. Repeating the request with the same key does not create a second payment. |
| Order | The merchant's purchase that a payment pays for (`order_id`). |
| Webhook | An HTTP call from the service to the merchant's server that reports an event. |
| RUT | Chile's tax id, with a check digit. |

## 7. Functional requirements

### Merchants

- **FR01** A merchant has a legal name, a RUT (validated with its check digit), a status (active or suspended) and a maximum amount per payment for each currency. In this iteration, merchants are loaded as seed data.
- **FR02** A merchant authenticates with a secret API key. The service stores only a hash of each key.
- **FR03** A suspended merchant cannot create payments, but it can still query them.
- **FR04** A merchant only sees its own payments: another merchant's payment answers `404`, as if it did not exist.

### Cards

- **FR05** The checkout tokenizes the card directly with the acquirer, from the browser. The service receives only the token: card numbers never reach its servers, its database or its logs.

### Payments

- **FR06** `POST /v1/payments` creates a payment for an order and authorizes it, with an `Idempotency-Key` header, the order id, the amount, the currency and the card token.
  - When the acquirer answers, the response is `201` with the final state.
  - When the answer is not clear, the response is `202` with the payment in `PROCESSING`; the service finishes it in the background (FR10 and FR11).
  - When the acquirer is unavailable, the response is `503` with `Retry-After`, and nothing is created (FR13).
  - Invalid input answers `400` and a broken business rule answers `422`; neither creates anything.
- **FR07** Idempotency keys are unique per merchant and do not expire, because they are kept with the payment.
  - The same key with the same request answers with the payment in its current state, `201` when it is final and `202` while it is processing, and with the header `Idempotent-Replayed: true`.
  - The same key with a different request answers `422 idempotency_key_reused`.
  - Concurrent requests with the same key create exactly one payment.
- **FR08** An order has at most one payment that is processing or approved. Paying such an order again, with another key, answers `409 order_has_active_payment`. After a decline or a failure, the order accepts a new payment with a new key.
- **FR09** A payment starts in `PROCESSING` and reaches one final state: `APPROVED`, `DECLINED` or `FAILED` (section 10). Final states never change. `APPROVED` and `DECLINED` are the acquirer's decision; `FAILED` is the service's, and it always carries a reason.
- **FR10** The first attempt runs inside the request, and every call carries the payment id as the idempotency key towards the acquirer.
  - Temporary errors are retried in the background, up to 3 attempts in total without a definite answer, with exponential backoff and jitter. They are a timeout, a lost connection, `5xx`, `408`, `429` or an unreadable answer.
  - A request the acquirer rejects, with any other `4xx`, ends `FAILED` with reason `acquirer_rejected`, without retries.
- **FR11** When the attempts run out and any of them may have reached the acquirer, the service reverses the authorization with the same key. It keeps trying until the acquirer confirms the reversal, and only then does the payment end `FAILED`, with reason `acquirer_unavailable`. A payment never ends `FAILED` while it may be authorized at the acquirer.
- **FR12** No authorization is sent more than 2 minutes after the payment was created. A payment still without a definite answer by then is handled as in FR11. A charge that arrives after the customer has left the checkout is worse than a clear failure.
- **FR13** When the acquirer keeps failing, the service stops calling it for a while (a circuit breaker). Meanwhile, new payments answer `503` with `Retry-After` and are not created, the payments in progress wait without spending attempts, and the service probes the acquirer before resuming.
- **FR14** `GET /v1/payments/{id}` returns a payment with its history. `GET /v1/payments` lists the merchant's payments, newest first, with filters by status and order id and cursor pagination.
- **FR15** Every status change and every call to the acquirer (attempt, outcome, latency and response code) is recorded in the payment's history, in the same transaction as the change.

### Webhooks

- **FR16** When a payment reaches a final state, the merchant receives a webhook signed with HMAC-SHA256 and a timestamp. Each event has an id, so the merchant can discard duplicates.
- **FR17** A failed delivery is retried with exponential backoff for 24 hours. After that, the delivery is marked failed; the merchant can still query the payment.

### Checkout and panel

- **FR18** A web app plays the merchant. It has a test checkout that tokenizes a card, pays an order and shows the result as soon as it is known, and a panel with:
  - the payment list with its filters;
  - each payment's history;
  - the webhooks received;
  - controls to make the simulated acquirer slow or unavailable.

## 8. Non-functional requirements

| ID | Requirement |
|---|---|
| NFR01 | The service adds at most 50 ms (p99) to the acquirer's latency. When the acquirer does not answer, `POST /v1/payments` answers within the acquirer's timeout plus that margin. |
| NFR02 | Target load: 500 payments per second sustained on one machine, with an acquirer that answers in 300 ms. Each API instance caps the payments it has in flight at the acquirer, answers `503` with `Retry-After` beyond that cap, and adding instances adds capacity. |
| NFR03 | If Kafka is down, payments are still authorized and answered. Their events wait in the outbox, and the webhooks are delivered when Kafka comes back. No event is lost. |
| NFR04 | If the acquirer is slow, a request waits at most the acquirer's timeout. If it is down, the breaker opens after a few failed calls; from then on, new payments are refused at once with `503` and nothing is created, while retries of existing payments still get their payment. The few payments created before the breaker opens answer `202` and are finished in the background. |
| NFR05 | Any instance may stop at any moment without losing a payment or authorizing one twice. |
| NFR06 | The service never receives, stores or logs a card number. |
| NFR07 | A payment can be followed in a single trace from the request to the webhook, and its logs carry the payment id and the trace id. |
| NFR08 | Deployments lose no requests and no payments: graceful shutdown and backward-compatible migrations. |
| NFR09 | `docker compose up` starts everything locally, with no account or secret to set up. |

## 9. Business rules

- Amounts are integers in the currency's minor unit: CLP has no decimals and USD is in cents. The currencies are CLP and USD.
- The minimum amount is 1. The maximum per payment is set per merchant and currency; the seed data uses 5,000,000 CLP and 5,000 USD.
- An `Idempotency-Key` has 1 to 255 printable ASCII characters, and an `order_id` has 1 to 64.
- The acquirer validates the card (the Luhn check and the expiry date) when it tokenizes it. The service never sees those fields.
- Timestamps are stored in UTC.
- The values in FR10 to FR13 (3 attempts, 2 minutes, the breaker's thresholds) are assumptions, to be tuned to the acquirer's real behavior. The RFC lists them.

## 10. Payment lifecycle

```mermaid
stateDiagram-v2
    [*] --> PROCESSING: the POST records it and calls the acquirer
    PROCESSING --> APPROVED: the acquirer approves
    PROCESSING --> DECLINED: the acquirer declines
    PROCESSING --> FAILED: rejected, or reversed after the attempts ran out
```

- `PROCESSING`: recorded and sent, or about to be sent, to the acquirer. It may be authorized there.
- `FAILED` reasons: `acquirer_rejected`, and `acquirer_unavailable`, which comes only after a confirmed reversal or when no attempt reached the acquirer.

## 11. Invariants

These are the guarantees the service must keep under any failure. Each one has a mechanism and a test in the [RFC](rfc/0001-architecture.md).

| ID | Invariant |
|---|---|
| INV01 | One idempotency key creates at most one payment per merchant. |
| INV02 | An order never has two payments that are processing or approved. |
| INV03 | A payment is authorized at the acquirer at most once, and a payment that ends `FAILED` or `DECLINED` leaves no live authorization at the acquirer. |
| INV04 | Every accepted payment reaches a final state, or raises an alert. |
| INV05 | What the merchant is told, by the API or by webhook, matches the database and the acquirer's book. |
| INV06 | Every final state produces one webhook event, delivered at least once and always with the same event id. |

## 12. Acceptance scenarios

These scenarios become end-to-end tests.

```gherkin
Feature: No duplicate payments

  Scenario: Double click with the same key
    Given an active merchant
    When it sends the same payment 20 times at once with key "k1"
    Then exactly one payment exists
    And all 20 responses carry the same payment id

  Scenario: The same key with a different amount
    Given a payment created with key "k1" for 10,000 CLP
    When the merchant sends key "k1" for 12,000 CLP
    Then the response is 422 "idempotency_key_reused"

  Scenario: The same order while a payment is live
    Given an approved payment for order "o1"
    When the merchant pays order "o1" with key "k2"
    Then the response is 409 "order_has_active_payment"

  Scenario: A new attempt after a decline
    Given a declined payment for order "o2"
    When the merchant pays order "o2" with a new key and another card
    Then the payment is accepted

Feature: The result in the response

  Scenario: An approval
    Given an active merchant and a card the acquirer approves
    When the merchant pays an order
    Then the response is 201 with the payment APPROVED and its authorization code

  Scenario: A decline
    Given a card the issuer declines
    When the merchant pays an order
    Then the response is 201 with the payment DECLINED and its decline code

Feature: Acquirer failures

  Scenario: A temporary error
    Given the acquirer answers 503 to the first attempt only
    When a payment is made
    Then the response is 202 with the payment PROCESSING
    And it ends APPROVED on the second attempt, in the background

  Scenario: A rejected request
    Given the acquirer answers 422 to the authorization
    When a payment is made
    Then the response is 201 with the payment FAILED "acquirer_rejected", after a single attempt

  Scenario: Timeout with a late approval
    Given the acquirer approves but answers after the timeout
    When a payment is made
    Then the response is 202 with the payment PROCESSING
    And the retry with the same key receives the same approval
    And the payment ends APPROVED with exactly one authorization at the acquirer

  Scenario: No answer at all
    Given the acquirer receives every attempt and never answers
    When a payment is made
    Then the service reverses the authorization
    And the payment ends FAILED "acquirer_unavailable" with no live authorization at the acquirer
    And a late authorization with the same key is rejected by the acquirer

  Scenario: The acquirer is down
    Given the acquirer refuses connections for 20 seconds
    When payments are made during that time
    Then the first payments answer 202, until the circuit breaker opens
    And from then on, new payments answer 503 with Retry-After without being created
    And a retry of a payment created before still gets that payment
    And once the acquirer is back, payments answer 201 APPROVED

Feature: Service failures

  Scenario: The API dies after sending
    Given a payment whose authorization was sent
    When the API instance stops before recording the answer
    Then the recovery takes the payment over when its lease expires
    And it ends APPROVED with exactly one authorization at the acquirer

  Scenario: The database fails after the approval
    Given the acquirer approved a payment
    When recording the approval fails
    Then the response is 202 with the payment PROCESSING
    And the recovery repeats the call with the same key, and the payment ends APPROVED

  Scenario: Kafka is down
    Given Kafka is unavailable for 3 minutes
    When merchants make payments during that time
    Then every request answers with the result, as usual
    And once Kafka is back, every webhook is delivered with no event lost

Feature: Webhooks

  Scenario: The merchant's endpoint is down
    Given the merchant's endpoint answers 500
    When a payment is approved
    Then the webhook is retried with increasing delays
    And once the endpoint recovers, it receives the signed event with the same event id

Feature: Merchants

  Scenario: A suspended merchant
    Given a suspended merchant
    When it creates a payment
    Then the response is 422 "merchant_suspended"

  Scenario: Another merchant's payment
    Given a payment of merchant "m1"
    When merchant "m2" asks for it
    Then the response is 404
```

## 13. How it will be proven

- Unit tests of the domain: the state machine, the classification of the acquirer's answers and the backoff.
- Integration tests against real Postgres and Kafka, with testcontainers.
- The acceptance scenarios above, as end-to-end tests against the full local environment, using the fault controls of the acquirer simulator.
- A consistency checker. After a load test with faults, it compares every payment in the database with the acquirer simulator's book and with the webhooks received, and checks INV02 to INV06.
- A load test with k6 for NFR01 and NFR02. The README reports what was measured, and on which machine.
- A Grafana dashboard that shows the service during the load test and its faults: latency, outcomes, the breaker, the recovery, the outbox and the webhooks.

## 14. Delivery

The RFC splits the work into pull requests (section 17). The README marks each capability as designed, implemented or proven.

## 15. Assumptions

- The acquirer is simulated, with its contract documented in the RFC.
- One country (Chile) and two currencies, with no conversion between them.
- The acquirer's tokenization is the only way a card enters the system, so the service never receives card data and its PCI scope stays minimal.

## 16. Open questions

- Should the request repeat a refused connection once before answering `202`? Proposal: no; the background repeats it within 1–2 s, and the request stays bounded by one call.
