// Load test for the three demo banks, written for k6 (https://k6.io).
//
//   k6 run load/k6-banks.js
//   k6 run -e VUS=40 -e DURATION=60s load/k6-banks.js
//
// What it does: every virtual customer signs in, then repeatedly reads its
// account and sends a KES 10 payment, while a few dashboards poll the operations
// and director pages the way open browser tabs do. Each customer pays a
// different account, so the run measures throughput and not the banks' fraud
// lockouts.
//
// It RESETS the demo first (setup() puts the director in "without Kifaru" mode),
// so run it against a throwaway instance, not in the middle of a rehearsal.
// Set -e RESET=0 to skip the reset. Override the addresses with -e NCBA_URL=...,
// KCB_URL, EQUITY_URL and NETWORK_URL if the services use other ports.
import http from "k6/http";
import { check, sleep } from "k6";

const url = {
  ncba: __ENV.NCBA_URL || "http://127.0.0.1:9101",
  kcb: __ENV.KCB_URL || "http://127.0.0.1:9102",
  equity: __ENV.EQUITY_URL || "http://127.0.0.1:9103",
  network: __ENV.NETWORK_URL || "http://127.0.0.1:9100",
};
// Each customer pays a different account so they do not trip each other's rules.
const customers = [
  { bank: "ncba", user: "grace", device: "grace-phone", toBank: "kcb", toAccount: "1105108" },
  { bank: "ncba", user: "peter", device: "peter-phone", toBank: "equity", toAccount: "0114417" },
  { bank: "kcb", user: "faith", device: "faith-phone", toBank: "ncba", toAccount: "1002291" },
  { bank: "equity", user: "kiprono", device: "kiprono-phone", toBank: "ncba", toAccount: "1007730" },
];
const json = { headers: { "Content-Type": "application/json" } };
const VUS = Number(__ENV.VUS || 20);
const DURATION = __ENV.DURATION || "30s";

export const options = {
  scenarios: {
    customers: {
      executor: "ramping-vus", exec: "customer", startVUs: 0,
      stages: [{ duration: "5s", target: VUS }, { duration: DURATION, target: VUS }, { duration: "5s", target: 0 }],
    },
    dashboards: { executor: "constant-vus", exec: "dashboard", vus: 5, duration: "40s" },
  },
  thresholds: {
    http_req_failed: ["rate<0.01"],
    checks: ["rate>0.99"],
    "http_req_duration{name:payment}": ["p(95)<500", "p(99)<1000"],
    "http_req_duration{name:account}": ["p(95)<300"],
    "http_req_duration{name:sign-in}": ["p(95)<500"],
    "http_req_duration{name:ops-state}": ["p(95)<300"],
    "http_req_duration{name:director-state}": ["p(95)<300"],
  },
};

export function setup() {
  if (__ENV.RESET === "0") return;
  const reset = http.post(`${url.network}/api/mode`, JSON.stringify({ mode: "without" }), json);
  check(reset, { "demo reset": (r) => r.status === 200 });
  // Give the mule account money to spend.
  http.post(`${url.equity}/interbank/credit`, JSON.stringify({
    ref: `K6-FUND-${Date.now()}`, from_bank: "ncba", from_bank_name: "NCBA", from_name: "k6",
    to_account: "0114417", amount: 1000000,
  }), json);
}

let session; // per virtual user

function signIn(plan) {
  const response = http.post(`${url[plan.bank]}/api/login`, JSON.stringify({
    username: plan.user, password: "bank1234", device: plan.device,
  }), { ...json, tags: { name: "sign-in" } });
  check(response, { "signed in": (r) => r.status === 200 });
  return response.json("token");
}

export function customer() {
  const plan = customers[(__VU - 1) % customers.length];
  if (!session) session = signIn(plan);
  const auth = { headers: { ...json.headers, Authorization: `Bearer ${session}` } };

  const me = http.get(`${url[plan.bank]}/api/me`, { ...auth, tags: { name: "account" } });
  check(me, { "account loads": (r) => r.status === 200 });
  if (me.status === 401) session = null; // signed out: sign in again next time

  const payment = http.post(`${url[plan.bank]}/api/transfers`, JSON.stringify({
    bank: plan.toBank, account: plan.toAccount, name: "k6 load test", amount: 10,
  }), { ...auth, tags: { name: "payment" } });
  check(payment, { "payment completed": (r) => r.status === 200 && r.json("status") === "completed" });
  sleep(0.3);
}

export function dashboard() {
  http.get(`${url.network}/api/state`, { tags: { name: "director-state" } });
  for (const bank of ["ncba", "kcb", "equity"]) {
    http.get(`${url[bank]}/api/ops/state`, { tags: { name: "ops-state" } });
  }
  sleep(1.2);
}
