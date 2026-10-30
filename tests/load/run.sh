#!/usr/bin/env bash
# The proof of step 9 (PRD, section 13), against the local environment, which
# must be up: `docker compose up --build --wait`.
#
#   1. Nominal: the acquirer answers in 300 ms, and k6 pays at each rate of
#      RATES for a minute (NFR01 and NFR02).
#   2. Faults: k6 pays with a mix of test cards at FAULT_RATE for 5 minutes,
#      while this script breaks the acquirer, Kafka, the API and the worker.
#   3. Consistency: once everything settles, the checker compares the
#      database, the acquirer's book and the webhooks (INV01 to INV06).
#
# Each phase writes its output to OUT. Grafana shows every phase live at
# localhost:3001.
set -euo pipefail
cd "$(dirname "$0")/../.."

RATES=${RATES:-"100 200 300 500"}
FAULT_RATE=${FAULT_RATE:-50}
OUT=${OUT:-tests/load/results}
mkdir -p "$OUT"

faults() { curl --fail --silent --show-error -X PUT localhost:8091/admin/faults -d "$1" >/dev/null; }
# --no-deps: k6 must never restart the services it measures.
k6() { docker compose --profile load run --rm --no-deps -e RATE="$1" -e DURATION="$2" -e MIX="$3" k6 run --quiet /scripts/payments.js; }
at() { echo "  $(date -u +%H:%M:%S) $*"; }

echo "== 1. Nominal: the acquirer answers in 300 ms"
faults '{"latency_ms":300}'
k6 50 30s clean >/dev/null # warm up: the first seconds of a fresh environment are slower
for rate in $RATES; do
  at "$rate payments per second for a minute"
  k6 "$rate" 1m clean >"$OUT/nominal-$rate.txt" 2>&1
  grep -E 'name:pay \}|dropped_iterations|payment_answers' "$OUT/nominal-$rate.txt" | sed 's/^/    /'
done

echo "== 2. Faults: $FAULT_RATE payments per second for 5 minutes, with a mix of cards"
faults '{"latency_ms":50}'
k6 "$FAULT_RATE" 5m faults >"$OUT/faults.txt" 2>&1 &
load=$!
sleep 30
at "the acquirer answers 503 to a third of its calls"
faults '{"latency_ms":50,"error_rate":0.3}'
sleep 40
at "it answers a fifth of its calls after the timeout"
faults '{"latency_ms":50,"timeout_rate":0.2}'
sleep 40
at "it goes down: the breaker opens, and new payments get 503"
faults '{"latency_ms":50,"down":true}'
sleep 30
at "it is back"
faults '{"latency_ms":50}'
sleep 15
at "Kafka stops: events wait in the outbox"
docker compose stop kafka >/dev/null 2>&1
sleep 45
at "Kafka is back"
docker compose start kafka >/dev/null 2>&1
sleep 20
at "payments-api is killed during its calls, and comes back"
docker compose kill payments-api >/dev/null 2>&1
docker compose up --detach --no-deps payments-api >/dev/null 2>&1
sleep 20
at "payments-worker is killed, and comes back"
docker compose kill payments-worker >/dev/null 2>&1
docker compose up --detach --no-deps payments-worker >/dev/null 2>&1
wait "$load" || true
grep -E 'name:pay \}|dropped_iterations|payment_answers' "$OUT/faults.txt" | sed 's/^/    /'

echo "== 3. Consistency, once everything settles"
go run ./tests/consistency -settle 8m | tee "$OUT/consistency.txt"
