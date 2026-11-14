// What a crowd of dashboard users does, for the hosted API.
//
//   node load/make-tokens.mjs https://kifaru-api.onrender.com > load/tokens.json
//   k6 run -e BASE=https://kifaru-api.onrender.com -e VUS=1000 load/k6-hosted.js
//
// One virtual user is one open dashboard: it loads the snapshot, then every few
// seconds does the small reads a tab does. Live updates arrive on the stream, so a
// tab does NOT reload the snapshot on every event; hold the streams with
// `go run ./load/sseload` at the same time to see the real mix.
//
// Pass: p95 < 800 ms, p99 < 2.5 s, fewer than 1% failed. Responses of 503 with
// Retry-After are deliberate load shedding and are counted separately.
import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter } from 'k6/metrics';
import { SharedArray } from 'k6/data';

const tokens = new SharedArray('tokens', () => JSON.parse(open(__ENV.TOKENS || './tokens.json')));
const BASE = (__ENV.BASE || 'http://127.0.0.1:8000').replace(/\/$/, '');
const VUS = Number(__ENV.VUS || 100);
const LIMIT = Number(__ENV.LIMIT || 500); // rows per history response; smaller means fewer bytes per user
const shed = new Counter('shed_503');

export const options = {
  scenarios: {
    dashboards: {
      executor: 'ramping-vus',
      stages: [
        { duration: __ENV.RAMP_UP || '2m', target: VUS },
        { duration: __ENV.DURATION || '5m', target: VUS },
        { duration: '30s', target: 0 },
      ],
      gracefulRampDown: '10s',
    },
  },
  thresholds: {
    'http_req_duration{kind:read}': ['p(95)<800', 'p(99)<2500'],
    http_req_failed: ['rate<0.01'],
  },
  summaryTrendStats: ['avg', 'med', 'p(95)', 'p(99)', 'max'],
};

const between = (lo, hi) => lo + Math.random() * (hi - lo);

export default function () {
  const user = tokens[(__VU - 1) % tokens.length];
  const params = (name) => ({ headers: { Authorization: `Bearer ${user.token}` }, tags: { kind: 'read', name }, responseCallback: http.expectedStatuses(200, 304, 503) });
  const inst = encodeURIComponent(user.inst);
  if (__ITER === 0) sleep(between(0, 10)); // spread VUs that start in the same tick

  const snapshot = http.get(`${BASE}/v1/history?institution=${inst}&limit=${LIMIT}`, params('history'));
  if (snapshot.status === 503) { shed.add(1); sleep(Number(snapshot.headers['Retry-After'] || 5) + between(0, 5)); return; }
  check(snapshot, { 'history 200': (r) => r.status === 200 });
  sleep(between(5, 15));

  check(http.get(`${BASE}/v1/notifications?institution=${inst}&limit=20`, params('notifications')), { 'notifications 200': (r) => r.status === 200 });
  sleep(between(5, 15));
  if (user.role === 'staff') {
    check(http.get(`${BASE}/v1/stats`, params('stats')), { 'stats 200': (r) => r.status === 200 });
  } else {
    check(http.get(`${BASE}/v1/stats/trends?days=30`, params('trends')), { 'trends 200': (r) => r.status === 200 });
  }
  sleep(between(10, 30));
}
