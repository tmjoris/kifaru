// Bank systems posting fraud reports: the ingestion API under load.
//
//   node load/make-tokens.mjs <base> > load/tokens.json
//   k6 run -e BASE=<base> -e RATE_START=10 -e RATE_END=60 load/k6-ingest.js
//   k6 run -e BASE=<base> -e PATH_=/v1/reports/batch -e BATCH=50 -e RATE_END=5 load/k6-ingest.js
//
// This WRITES real rows (marked 'Load test report'); point it at a test database.
// A 503 means the service shed the work on purpose: slow down or retry later.
import http from 'k6/http';
import { check } from 'k6';
import { SharedArray } from 'k6/data';
const tokens = new SharedArray('t', () => JSON.parse(open(__ENV.TOKENS || './tokens.json')).filter((x) => x.role === 'institution'));
const insts = new SharedArray('i', () => [...new Set(JSON.parse(open(__ENV.TOKENS || './tokens.json')).filter((x) => x.role === 'institution').map((x) => x.inst))]);
const BASE = __ENV.BASE || 'http://127.0.0.1:8000';
const START = Number(__ENV.RATE_START || 2), END = Number(__ENV.RATE_END || 20), DUR = __ENV.DUR || '40s', HOLD = __ENV.HOLD || '20s';
const PATH = __ENV.PATH_ || '/v1/reports';
export const options = {
  scenarios: { ingest: { executor: 'ramping-arrival-rate', startRate: START, timeUnit: '1s', preAllocatedVUs: 100, maxVUs: 2000,
    stages: [{ duration: DUR, target: END }, { duration: HOLD, target: END }] } },
  summaryTrendStats: ['avg', 'med', 'p(95)', 'p(99)', 'max'],
};
const hex = (n) => { let s = ''; while (s.length < n) s += Math.random().toString(16).slice(2); return s.slice(0, n); };
function report(user) {
  let dest = insts[Math.floor(Math.random() * insts.length)];
  if (dest === user.inst) dest = insts[(insts.indexOf(dest) + 1) % insts.length];
  return {
    reporting_institution: user.inst, destination_institution: dest, reporting_system: 'Bank connector',
    transaction_ref: `LOAD-${__VU}-${__ITER}-${hex(10)}`, transaction_timestamp: new Date().toISOString(),
    subject_account_hash: `sha256:${hex(64)}`, subject_customer_hash: `sha256:${hex(64)}`, destination_account_hash: `sha256:${hex(64)}`,
    amount: 5000 + Math.floor(Math.random() * 900000), currency: 'KES', channel: 'instant_payment', bank_risk_score: 0.8,
    risk_codes: ['MUL-440', 'ATO-460', 'VEL-429'],
    evidence: { account_age_days: 3, distinct_senders_7d: 8, is_new_device: true, is_new_beneficiary: true, velocity_1h: 6 },
    narrative: 'Load test report',
  };
}
export default function () {
  const u = tokens[Math.floor(Math.random() * tokens.length)];
  const body = PATH.endsWith('batch') ? JSON.stringify(Array.from({ length: Number(__ENV.BATCH || 25) }, () => report(u))) : JSON.stringify(report(u));
  const r = http.post(`${BASE}${PATH}`, body, { headers: { Authorization: `Bearer ${u.token}`, 'X-Kifaru-CSRF': u.csrf, 'Content-Type': 'application/json' } });
  check(r, { 'accepted': (x) => x.status === 200 });
}
