# ADR 0003: Migrations run as their own step, with goose

- **Status:** accepted
- **Date:** 2026-10-28

## Context

Every service runs several replicas. If each replica migrated its database when it started, as v1 did with its single instance, the replicas would race to change the schema, and every deployment would also be a schema change. NFR11 also requires deployments without downtime, so the old version of a service must keep working while the new schema is applied.

## Decision

- A `migrate` command applies a database's migrations: `migrate <database> up`. The services never migrate.
- It runs before the services start: as a one-shot service in Compose, and as a Kubernetes Job in milestone 4.
- It uses goose's provider with a Postgres advisory lock, so only one migrator changes a database at a time; the others wait and then find nothing left to apply.
- Migrations are plain SQL files in `migrations/<database>/`, embedded in the binary.
- Changes follow expand and contract: a migration never breaks the code that is still running. New columns and tables come first; columns are removed only after no code uses them.

## Consequences

- No races between replicas, and the schema change is a visible step of every deployment.
- Schema changes are reviewed as SQL.
- Removing or renaming a column takes two deployments.

## Alternatives considered

- **Migrating when each service starts:** simple with one instance, unsafe with several.
- **A declarative tool such as Atlas:** it computes the changes from a desired schema, but plain SQL files are easier to review and need less tooling.
