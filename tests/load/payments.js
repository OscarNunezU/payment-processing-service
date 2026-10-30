// The load test of step 9 (PRD, section 13): payments at a constant rate, each
// one tokenized at the acquirer and paid through the API, as a checkout does.
//
//   RATE      payments per second (500, NFR02)
//   DURATION  how long, such as 2m
//   MIX       clean: approving cards only; faults: a mix of the test cards
//
// tests/load/run.sh runs it inside Compose, where the hostnames below resolve.
import http from "k6/http";
import { check } from "k6";
import exec from "k6/execution";
import { Counter } from "k6/metrics";

const API = __ENV.API_URL || "http://payments-api:8080";
const ACQUIRER = __ENV.ACQUIRER_URL || "http://acquirer-sim:8090";
// The local keys of the README, for local development only.
const SECRET_KEY = __ENV.API_KEY || "sk_test_local_tienda_uno";
const PUBLISHABLE_KEY = __ENV.PUBLISHABLE_KEY || "pk_sim_local_only";
const RATE = Number(__ENV.RATE || 500);
const MIX = __ENV.MIX || "clean";
// Keys and orders unique to this run, so runs can follow each other.
const RUN = __ENV.RUN || String(Date.now());

// With faults, most cards approve; the rest decline, fail once, answer late,
// never answer or are rejected (RFC 0001, section 10).
const FAULTY = [
  ...Array(14).fill("4111111111111111"),
  "4000000000000002",
  "4000000000009995",
  "4000000000000127",
  "4000000000000135",
  "4000000000000150",
  "4000000000000143",
];

export const options = {
  scenarios: {
    payments: {
      executor: "constant-arrival-rate",
      rate: RATE,
      timeUnit: "1s",
      duration: __ENV.DURATION || "2m",
      // With faults, many calls wait for the 5 s timeout, so many more
      // payments are in flight, and k6 would drop some while it starts VUs.
      preAllocatedVUs: Math.max(50, MIX === "faults" ? RATE * 4 : RATE),
      maxVUs: Math.max(200, RATE * 4),
    },
  },
  summaryTrendStats: ["avg", "p(50)", "p(95)", "p(99)", "max"],
  // A threshold per kind of request makes the summary report each one, with
  // exact percentiles. They are wide on purpose: the run reports, it does not
  // fail.
  thresholds: {
    "http_req_duration{name:pay}": ["p(99)<60000"],
    "http_req_duration{name:tokenize}": ["p(99)<60000"],
  },
};

const answers = new Counter("payment_answers");

export default function () {
  const card = MIX === "faults" ? FAULTY[exec.scenario.iterationInTest % FAULTY.length] : FAULTY[0];
  const token = http.post(`${ACQUIRER}/v1/tokens`, JSON.stringify({ number: card, exp_month: 12, exp_year: 2035, cvc: "123" }), {
    headers: { Authorization: `Bearer ${PUBLISHABLE_KEY}`, "Content-Type": "application/json" },
    tags: { name: "tokenize" },
  });
  if (token.status !== 201) {
    answers.add(1, { status: `token ${token.status}` });
    return;
  }

  const key = `${RUN}-${exec.scenario.iterationInTest}`;
  const res = http.post(
    `${API}/v1/payments`,
    JSON.stringify({ order_id: `load-${key}`, amount: 15000, currency: "CLP", card_token: token.json("token") }),
    {
      headers: { Authorization: `Bearer ${SECRET_KEY}`, "Content-Type": "application/json", "Idempotency-Key": key },
      tags: { name: "pay" },
    },
  );
  answers.add(1, { status: String(res.status) });
  check(res, { "answered 201, 202 or 503": (r) => r.status === 201 || r.status === 202 || r.status === 503 });
}
