# Roadmap

What could come after the first iteration, in a likely order. Each item says what it adds and what it would change in the design. Nothing here is built.

1. **Capture, void and refunds.** Authorization and capture as separate steps, an automatic void of authorizations that are never captured, and partial refunds. Each call to the acquirer becomes an operation row, written before it is sent, as attempts are today.
2. **Metrics and alerts.** Prometheus metrics for the outbox backlog, consumer lag, acquirer latency and approval rate, with dashboards and alerts on SLOs.
3. **Risk.** Velocity limits per card and per IP, blocklists and card-testing detection. They run inside the request, before the authorization, so they keep working when Kafka is down.
4. **Merchant portal.** Login, users with roles, API key rotation, webhook endpoints managed by the merchant, and resending events.
5. **A second acquirer, with safe failover.** Routing by card brand, cost and health. Failing over is only safe once the first acquirer certainly holds no live authorization: after a refused connection, or after a confirmed reversal. A TLA+ model of that protocol was started on the `spec/authorization-model` branch. Writing it found the need for reversals recorded before they are sent, epochs that fence out stale workers, and tombstones at the acquirer; this iteration's design already includes all three.
6. **Money: ledger, settlement and payouts.** A double-entry ledger fed by the payment events, settlement files from the acquirer reconciled against the payments and the bank, and payouts per merchant with a cut-off time. This is where another consumer of the payment events adds the most.
7. **Production.** Kubernetes, three Kafka brokers, a Postgres primary with replicas, chaos tests and recovery drills.
