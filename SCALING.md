# Scaling Kifaru on Render's free tier and Neon

What the platform can carry, what was changed to get there, how to measure it, and
what is still open. Every number marked *measured* came from the test rig described
below, not from a guess.

## The hosting limits (checked 2026-11-14)

| | Limit | Source |
| --- | --- | --- |
| Render free web service | 512 MB RAM, 0.1 CPU, one instance, no scaling | [Render article](https://render.com/articles/from-side-project-to-production-scaling-your-first-app), [pricing](https://render.com/pricing) |
| Render free spin-down | After 15 minutes without traffic; about a minute to wake | [Render FAQ](https://render.com/docs/faq) |
| Render free hours | 750 instance-hours per workspace per month | Render pricing and FAQ |
| Neon free storage | 1 GB per project (older pages that say 0.5 GB are out of date) | [Neon plans](https://neon.com/docs/introduction/plans), [free-plan FAQ](https://neon.com/faqs/free-plan-limits-and-quotas) |
| Neon free compute | 100 CU-hours per project per month; suspends after 5 idle minutes; up to 2 CU | same |
| Neon connections | 104 `max_connections`, 7 reserved, so 97 usable on a direct connection; the built-in pooler accepts up to 10,000 clients | same |

Not found in these sources: Render's free outbound bandwidth allowance and Neon's
network-transfer allowance. Check both pages before a long public test.

Two consequences:

- **Compute hours are a budget.** At 0.25 CU, 100 CU-hours is about 400 hours of
  running time, but only if the database is allowed to sleep. A sustained 10,000-user
  test keeps it awake and spends the allowance quickly. When it runs out, Neon
  suspends the project and every connection drops.
- **Storage.** Measured: 375,000 reports used 435 MB with indexes, about 1.2 KB each,
  so 1 GB holds about 850,000 reports. At thousands a day that is months; at
  millions a day it is hours. Turn on retention (below) or move to a paid plan.

## What one tenth of a CPU means

Render gives about 100 ms of CPU per second. The old code spent about 150 ms of CPU
on one dashboard load, so one instance served about **one dashboard load a second**.
Nothing about 10,000 users is possible without making each request cost far less CPU,
and without removing the per-user polling that multiplied the load.

## What changed

| Change | Why | Where |
| --- | --- | --- |
| Verified sessions cached in memory; last-seen written in one batched UPDATE | Every request ran a three-table join plus an UPDATE | `sessions.go` |
| Indexes on `(institution, submitted_at)`; newest ids picked before joining | `/v1/history` sorted whole tables: 338 ms per institution, 1.9 s for staff | `migrations/010_scale_indexes.sql`, `handlers.go` |
| History JSON built in the database; response cache with request coalescing, background refresh, ETag/304, one gzip per build | Turning 2,000 rows into Go maps and back was most of the CPU | `cache.go`, `handlers.go` |
| `/v1/stats` as three single-pass queries run side by side, cached | Fourteen sequential queries, about 1.5 s | `handlers.go` |
| Push stream: tabs load once, then receive changed rows; reconnects are replayed from a cursor | Every open tab used to reload the whole dashboard on every event | `hub.go`, `events.go`, `src/api.ts`, `src/App.tsx` |
| Streams hijack their connection and keep only a `net.Conn` | net/http costs about 36 KB per stream; 10,000 would not fit | `hub.go`, `events.go` |
| Runtime sized from the container's cgroup (GOMAXPROCS, memory limit, GOGC) | Go saw the host's cores and doubled its heap into an OOM kill | `tuning.go` |
| Report ingestion: cached config, one statement for report, artefacts, validation and audit; one transaction per 100 reports in the batch endpoint | About 25 database round trips per report | `ingest.go` |
| Admission control: `LOGIN_CONCURRENCY` (4) and `INGEST_CONCURRENCY` (64); excess gets 503 with `Retry-After` | Without it, overload makes every request time out together | `auth.go`, `limits.go` |
| Optional retention pruning (`RETENTION_DAYS`) | Neon's storage cap | `limits.go` |
| Pool of 60 connections (`DB_MAX_CONNS`) | At an 80 ms database round trip, 20 connections limited ingestion to 19 reports a second | `main.go` |

## Measured results

Rig: Postgres 16 with TLS, a TCP proxy adding the stated round-trip time, and the API
in a container limited to 0.1 CPU and 512 MB, with 375,000 reports loaded. The same
tests ran against the old and the new code.

| Test | Before | After |
| --- | --- | --- |
| Full dashboard loads per second | about 1 | about 22 |
| 10,000 held live streams | not possible (about 360 MB projected from per-connection cost) | 73–127 MiB, about 1% CPU, all 10,000 connected |
| Report ingestion, 4 ms round trip | collapsed at about 25 a second | 60 a second with p95 1.3 s; accepts about 65–85 a second and sheds the rest in under a second |
| Report ingestion, 80 ms round trip, pool of 20 | not measured | 19 a second |
| Report ingestion, 80 ms round trip, pool of 60 | not measured | about 37 a second |

### Hosted results (Render and Neon, run 2026-11-14 from a home PC, after the merge)

| Test | Result |
| --- | --- |
| 100 dashboard users, 90 s | no failures; p95 850 ms (984 ms on the old code). Most of that is the network trip: the median is 259 ms |
| 2,000 live streams | 1,998 connected, none turned away, live batches delivered, `/healthz` steady at about 0.4 s |
| **10,000 live streams** | **9,999 held** for the whole run, none turned away, 18,012 pushed batches delivered |
| 10,000 streams + 1,000 dashboard users + ingestion at up to 20 a second | streams held; ingestion 13.8 a second, 98.9% accepted, p95 6.6 s; **dashboard users failed: 17.6% errors, p95 58 s**; `/healthz` slowed to 15-19 s at the peak and recovered |
| 1,000 dashboard users alone (no streams) | **failed too: 15.7% errors, p95 58 s**, so the streams are not the cause |
| Same 1,000-user script against the local 0.1 CPU rig, from the same PC | no failures; p95 2.6 s; container at 1.9% CPU |

What this shows: the hosted service holds 10,000 idle-ish live streams. It does **not**
yet serve 1,000 simultaneously active dashboard users within the target, and the cause is
not the code's CPU use (the rig passes at 1.9% CPU). The remaining suspects, none of
them tested: Render's proxy or its CPU throttling on the free tier, Neon's latency or a
cold compute, and the test PC's own downlink and uplink while 1,000 simulated browsers
download at once. Running the same script from a cloud machine near Render would
separate the client from the server.

### What these numbers do not say

- On the hosted run the streams were not idle: the demo event producer pushed batches
  throughout (18,012 frames delivered), but never a steady flow of thousands of reports
  a minute.
- The rig's database is on the same machine, so its queries are faster than Neon's.
  The round-trip time is simulated, not Neon's real latency.
- The hosted 10,000 streams were held for about 4 minutes, not hours; heartbeats
  every 25 s are meant to keep Render's proxy from closing them, but a long hold is untested.
- The new frontend has been typechecked but not run in a browser.
- Ingestion still costs about six database round trips per report. At the 80 ms
  round trip of a distant Neon region that is the ceiling, not the CPU. Fewer
  round trips (a single stored procedure for scoring) is the next lever.

## Settings

| Variable | Default | Effect |
| --- | --- | --- |
| `DB_MAX_CONNS` / `DB_MIN_CONNS` | 60 / 2 | Pool size. Neon allows 97 direct connections; use the `-pooler` endpoint if you run more than one instance |
| `DB_QUERY_MODE` | prepared | `exec` for a transaction-mode pooler that does not support prepared statements |
| `LOGIN_CONCURRENCY` | 4 | Password checks at once; a check costs about 250 ms of CPU |
| `INGEST_CONCURRENCY` | 64 | Report submissions at once |
| `STREAM_MAX` | 12000 | Open live streams before new ones get 503 |
| `RETENTION_DAYS` | off | Delete reports expired this many days ago, hourly, in batches |
| `GOMAXPROCS`, `GOMEMLIMIT`, `GOGC` | from the container | Override the automatic sizing |
| `PPROF_ADDR` | off | e.g. `127.0.0.1:6060` to profile CPU |

Point Render's health check at `/healthz`. It does not touch the database, so the
check does not wake Neon.

## Running the tests yourself

Install [k6](https://grafana.com/docs/k6/latest/set-up/install-k6/) and Go.

```
node load/make-tokens.mjs https://kifaru-api.onrender.com > load/tokens.json

# a crowd of dashboard users (start small, then raise VUS)
k6 run -e BASE=https://kifaru-api.onrender.com -e VUS=200 load/k6-hosted.js

# hold live streams (ramp slowly; opening thousands of TLS connections a second makes the client the limit)
go run ./load/sseload -base https://kifaru-api.onrender.com -tokens load/tokens.json -n 2000 -rate 100

# report ingestion: this WRITES rows, so use a test database
k6 run -e BASE=<base> -e RATE_START=10 -e RATE_END=60 load/k6-ingest.js
```

`load/tokens.json` holds live session tokens and is git-ignored. On Windows, run the
same commands in PowerShell. A 10,000-stream test from a home connection is limited
by your own machine's file descriptors and uplink before it is limited by the server.

## Still open

1. Find why 1,000 simultaneously active dashboard users fail on the hosted service when
   the rig passes: rerun from a cloud machine near Render, and check Render's CPU and
   Neon's compute state during the run.
2. Measure delivery with 10,000 streams and a steady flow of thousands of reports a minute.
3. Cut ingestion round trips further (about six per report today); only a test against
   the real Neon region can show how much that is worth.
4. Click through the dashboard in a browser with the new live stream.
5. Cross-check Render's and Neon's bandwidth and transfer allowances.
6. The load test wrote about 2,000 'Load test report' rows into the hosted database.
   Remove them, or turn on `RETENTION_DAYS`.
