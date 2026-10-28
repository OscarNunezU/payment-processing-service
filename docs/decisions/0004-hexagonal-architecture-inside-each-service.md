# ADR 0004: Hexagonal architecture inside each service

- **Status:** accepted
- **Date:** 2026-10-28

## Context

RFC 0001 defines the architecture between services: the synchronous payment path, Kafka as the event backbone and the vault as the PCI boundary. This ADR defines how the code inside each service is organized.

The authorization protocol is the part of the platform that must be proven: with a TLA+ model, property tests and fault injection. Its rules have to be testable without HTTP or SQL. The same rules also have more than one entry point: the API and the recovery worker run the same protocol. Milestone 2 adds a second acquirer that speaks a different protocol behind the same operations.

## Decision

Each service follows ports and adapters (hexagonal architecture), as v1 did, with the domain kept apart from the use cases:

```
internal/payments/
  domain/       Entities, value objects and rules: the payment and operation
                state machines, amounts, installments, merchant rules.
                Plain Go: no I/O and no context.
  app/          Use cases (authorize, capture, void, recover) and the ports
                they need: Store, Acquirer, RiskCounters, Clock, IDs.
  adapters/
    http/       Inbound: the public API.
    worker/     Inbound: recovery and sweeps.
    postgres/   Outbound: Store.
    vault/      Outbound: Acquirer, through the vault's forward proxy.
    valkey/     Outbound: RiskCounters.
cmd/payments-api, cmd/payments-worker
                Wiring, by hand: build the adapters and pass them to the use cases.
```

The rules:

1. **Dependencies point inward.** `domain` imports only the standard library. `app` imports `domain`. Adapters import `app` and `domain`. Only `cmd` knows every piece.
2. **`internal/platform` is for adapters and `cmd`.** It holds technical helpers (HTTP, Postgres, the outbox, telemetry). The domain and the use cases never import it.
3. **Transactions live in the Store adapter, shaped like the protocol.** Store methods match the transactions of RFC 0001: one method for T1, which writes the idempotency record, the order, the payment, the operation, the events and the outbox rows together, and one for T2, which records the result with a conditional update. They are not one method per table. The domain decides each transition, and the adapter applies it only if the row is still in the expected state, so a concurrent change is detected instead of overwritten.
4. **Postgres is the last line of defense.** The domain checks the rules first, to return clear errors. The constraints in the database (RFC 0001, section 7) enforce them anyway, so a bug in the code cannot break an invariant.
5. **Services never import each other's packages.** They talk through their APIs and events (ADR 0001).

The linter enforces rules 1, 2 and 5 with `depguard` (`.golangci.yml`), so a violation fails CI.

## Consequences

- The domain is tested with unit and property tests, without mocks.
- The use cases are tested with in-memory implementations of the ports, as in v1.
- The adapters are tested against real Postgres, Valkey and Kafka with testcontainers.
- The API and the worker share one implementation of the protocol.
- Acquirer B (ISO 8583) is a second adapter behind the same `Acquirer` port.
- There are more packages and some mapping between layers. For a simple service, such as the bank simulator, a single package is enough; this structure is for services with domain rules.

## Alternatives considered

- **One package per service:** less code, but HTTP, SQL and rules would mix, and the protocol could not be tested on its own.
- **v1's compact version (`core` and `adapters`):** it worked for a single service. Here the domain is large enough to keep apart from the use cases, so that its property tests need no ports at all.
- **Strict Clean Architecture, with more layers:** more mapping and more indirection, without a gain this project needs.
