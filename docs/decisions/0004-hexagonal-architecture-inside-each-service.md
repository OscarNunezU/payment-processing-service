# ADR 0004: Hexagonal architecture inside each service

- **Status:** accepted
- **Date:** 2026-10-28
- **Updated:** 2026-10-28, when the first iteration was narrowed (RFC 0001); 2026-10-29, for the authorization in the request (ADR 0005)

## Context

RFC 0001 defines the architecture between components: the API, Kafka, the workers and the acquirer. This ADR defines how the code inside each service is organized.

The processing of a payment is the part of the service that must be proven, with unit tests and with failure tests. Its rules have to be testable without HTTP, SQL or Kafka. The same rules also have more than one entry point: the API authorizes inside the request, and the recovery finishes in the background what a request could not, and both move payments through the same states. And a second acquirer, which is in the roadmap, would sit behind the same operations.

## Decision

Each service follows ports and adapters (hexagonal architecture), as v1 did, with the domain kept apart from the use cases:

```
internal/payments/
  domain/       Entities, value objects and rules: the payment state
                machine, the classification of the acquirer's answers,
                the backoff, the authorization window, amounts and
                merchant rules. Plain Go: no I/O and no context.
  app/          Use cases (accept and authorize, recover, reverse) and the
                ports they need: Store, Acquirer, Clock, IDs.
  adapters/
    httpapi/    Inbound: the public API.
    recovery/   Inbound: the payments whose lease expired, from Postgres.
    postgres/   Outbound: Store.
    acquirer/   Outbound: Acquirer, the HTTP client with its circuit breaker.
cmd/payments-api, cmd/payments-worker
                Wiring, by hand: build the adapters and pass them to the use cases.
```

The rules:

1. **Dependencies point inward.** `domain` imports only the standard library. `app` imports `domain`. Adapters import `app` and `domain`. Only `cmd` knows every piece.
2. **`internal/platform` is for adapters and `cmd`.** It holds technical helpers (HTTP, Postgres, Kafka, the outbox and telemetry). The domain and the use cases never import it.
3. **Transactions live in the Store adapter, shaped like the processing.** Store methods match the transactions of RFC 0001, section 5: T1, which records the payment and its first attempt; T2, which records the answer with its history and its outbox row; the claim of the recovery; and the start of a reversal. They are not one method per table. The domain decides each transition, and the adapter applies it only if the row is still in the expected state and epoch, so a concurrent change is detected instead of overwritten.
4. **Postgres is the last line of defense.** The domain checks the rules first, to return clear errors. The constraints in the database (RFC 0001, section 7) enforce them anyway, so a bug in the code cannot break an invariant.
5. **Services never import each other's packages.** They talk through their APIs and events (ADR 0001).

The linter enforces rules 1, 2 and 5 with `depguard` (`.golangci.yml`), so a violation fails CI.

## Consequences

- The domain is tested with unit tests, without mocks.
- The use cases are tested with in-memory implementations of the ports, as in v1.
- The adapters are tested against real Postgres and Kafka with testcontainers.
- The API and the recovery share one implementation of the rules.
- A second acquirer would be another adapter behind the same `Acquirer` port.
- There are more packages and some mapping between layers. For a simple service, such as the acquirer simulator, a single package is enough; this structure is for services with domain rules.

## Alternatives considered

- **One package per service:** less code, but HTTP, SQL, Kafka and rules would mix, and the processing could not be tested on its own.
- **v1's compact version (`core` and `adapters`):** it worked for one service. Here the domain is kept apart from the use cases so that its tests need no ports at all.
- **Strict Clean Architecture, with more layers:** more mapping and more indirection, without a gain this project needs.
