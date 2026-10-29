# ADR 0001: One Go module, with one main per deployable

- **Status:** accepted
- **Date:** 2026-10-28
- **Updated:** 2026-10-28, when the first iteration was narrowed (RFC 0001)

## Context

The service has several deployables that are deployed and scaled on their own (`payments-api`, `payments-worker` and `webhooks`), plus the acquirer simulator. All of them are written in Go and share the same platform code: configuration, logging, telemetry, Postgres, the outbox, Kafka and HTTP.

## Decision

- One repository and one Go module.
- One `main` per deployable in `cmd/`. Each service's code lives in `internal/<service>`, and what they share lives in `internal/platform`.
- One container image with every binary. Each service runs the binary it needs.
- Services only talk to each other through their APIs and events, never by importing another service's internal packages.

## Consequences

- A change that crosses services, such as a new event and its consumer, is a single pull request with a single CI run.
- Every service uses the same version of every dependency.
- A dependency update reaches every service at once, so the whole suite has to pass.
- The image carries every binary. They are small static binaries, so the cost is a few megabytes.
- The linter enforces that services do not import each other, with `depguard` rules in `.golangci.yml`.

## Alternatives considered

- **Several modules joined by a Go workspace:** independent dependency versions per service, at the cost of more files and more tooling for one developer.
- **One repository per service:** a change that crosses services would need several coordinated pull requests.
