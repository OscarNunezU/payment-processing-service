# ADR 0006: Observability with OpenTelemetry, Prometheus, Tempo and Grafana

- **Status:** accepted
- **Date:** 2026-10-29

## Context

Until step 8, the services sent traces to Jaeger over OTLP, and there were no metrics: RFC 0001 left them for the roadmap, and k6 was to report latency and throughput alone.

Two things changed that:

- Step 9 proves the service under load and failures. A number in the README says what happened at the end; it does not show the breaker opening, the recovery draining its payments, or the outbox growing while Kafka is down and draining afterwards. Those are the behaviors this service exists to have.
- INV04 says every accepted payment reaches a final state or raises an alert. The recovery's alert was a log line at level `ERROR`, which nothing evaluates.

The instrumentation is already OpenTelemetry, so the choice is where the signals go, not how they are produced. Most companies that run Kubernetes keep their metrics in Prometheus and look at them in Grafana; for traces, Jaeger and Tempo are both common, and many companies pay for a hosted product that also receives OTLP.

## Decision

- **Instrumentation stays OpenTelemetry**, now for metrics too. The HTTP middleware and client record request durations. The adapters and `cmd` add the metrics of the processing: the acquirer's answers, payments created and finished, the recovery's attempts and reversals, payments in progress and unresolved, the breaker, the outbox, and the webhooks. The domain and the use cases stay free of them (ADR 0004): the store counts what it committed.
- **Both signals are pushed over OTLP**, one endpoint each (`OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`, `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT`). Metrics go to Prometheus's OTLP receiver every 10 s. Nothing is scraped, so no service opens a metrics port, and the API keeps a single, public one.
- **Traces move from Jaeger to Tempo**, so one Grafana shows metrics and traces together: a latency point jumps to the trace of the request it measured, and Tempo derives the service graph from the traces.
- **Grafana starts provisioned** from files in the repository: the data sources, the dashboard, and no login locally. Prometheus evaluates the alert rules, starting with INV04's.
- The HTTP durations get fine buckets around the acquirer's 5 s timeout, so a percentile can tell a call cut at the timeout from a slower one (NFR01).

## Consequences

- `docker compose up` starts Grafana at `localhost:3001`, with the dashboard as its home page.
- Three containers replace one: Tempo, Prometheus and Grafana for Jaeger.
- The services do not depend on the vendor: changing the backend, for example to a hosted product, changes two variables.
- Locally, Tempo and Prometheus keep a day of data on the container. Production would keep traces in object storage and size the metrics' retention.
- Every metric is counted in one place, and the gauges that come from the database are queried every 10 s, which is cheap at this scale.

## Alternatives considered

| Alternative | Why not |
|---|---|
| Keep Jaeger, and add Prometheus and Grafana | It works, since Grafana reads Jaeger, but two tools would show traces, and Jaeger's metrics links are weaker. Moving to Tempo costs a container and a configuration file, and no code. |
| Scrape `/metrics` from each service | The classic Prometheus model, but `payments-api` would need a second port, kept off the internet. Pushing over OTLP mirrors the traces and needs no port. |
| An OpenTelemetry Collector between the services and the backends | Useful to route and sample at scale; here it would be one more container between the services and two backends that already speak OTLP. |
| A hosted product (Datadog, Honeycomb, New Relic) | It needs an account and a key, which the local environment must not (NFR09). The instrumentation would not change. |
| Metrics in the roadmap, as before | The proof of step 9 would be numbers without the behavior behind them, and INV04's alert would stay a log line. |
