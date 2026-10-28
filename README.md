# payment-processing-service

A payments platform for merchants: it authorizes and captures card payments through two acquirers, records every money movement in a double-entry ledger, reconciles the acquirers' settlements and pays each merchant. It is designed so that no failure can lose or duplicate money without being detected.

> **Status:** design. Milestone 1 (reliable core) starts once the PRD and the architecture RFC are approved.

## Documents

- [PRD](docs/prd.md): what the platform does and why.
- [Architecture RFC](docs/rfc/0001-architecture.md): how it is built, and the plan for milestone 1.

## Milestones

| Milestone | Status |
|---|---|
| 1. Reliable core | Design |
| 2. Money adds up | Not started |
| 3. Merchant and operations | Not started |
| 4. Production | Not started |

## Status labels

Every capability in this repository is marked as one of:

- **Designed:** described in the PRD or the RFC, not built yet.
- **Implemented:** built and covered by automated tests.
- **Proven:** demonstrated under load or failure, with the results in this repository.

Today, everything is designed.
