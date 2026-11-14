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

### Hosted results (Render and Neon, 2026-11-14, from one home PC with 2 cores)

| Test | Client saw | Server saw (`/v1/admin/metrics`) |
| --- | --- | --- |
| 2,000 live streams | 1,998 connected, none turned away | n/a |
| **10,000 live streams** | **9,999 held** for the whole run, 18,012 pushed batches delivered | n/a |
| 300 dashboard users, 500-row snapshots | p95 1.14 s | 6% CPU, 0 throttling, 0 slow handlers |
| 1,000 dashboard users, 500-row snapshots | **p95 41-58 s, 7-16% failed** | 10% CPU, 0 throttling, 0 handler calls over 1 s, no database waits, 46 MiB memory |
| 1,000 dashboard users, 50-row snapshots | **p95 485 ms, 0% failed** | 13% CPU, 0 throttling, 0 slow handlers |
| Same 1,000-user script against the local 0.1 CPU rig | p95 2.6 s, no failures | 1.9% CPU |

How the 1,000-user failure was traced, and what it does and does not prove:

- **The Go process was not the bottleneck.** While clients saw p95 of 41 s, the server
  handled 7,139 requests with no handler call over a second, 10% CPU and essentially no
  CPU throttling or database waiting. The delay was outside it.
- **The time was spent downloading.** Client-side, the body download averaged 7.8 s
  (p95 52.8 s) while waiting for the first byte averaged 0.7 s.
- **Fewer bytes fixed it.** Cutting response size (average 166 KB to 44 KB per request)
  took the same 1,000 users from failing to a clean pass. The first run pulled 755 MB in
  about two minutes (roughly 50 Mbps) through one home connection into a 2-core machine
  at load average 2.7.
- **What is not proven:** whether the limit was this PC's CPU, this PC's link, or Render's
  outbound throughput to a single address. All three produce the same picture and the
  test cannot separate them. Real users are separate browsers on separate links, so one
  home connection concentrating 1,000 of them is a worse case than production. A load
  generator on a cloud host near Render would settle it.
- **The product implication holds either way.** The dashboard asks for 2,000 history rows
  on every full load, and a staff snapshot of 500 rows is already 163 KB compressed. At
  10,000 users, snapshot size is the dominant outbound cost on a free plan. Loading fewer
  rows first and fetching older ones on demand (the live stream now keeps the top of the
  list current) would cut that sharply. That change is not made.

### What these numbers do not say

- On the hosted run the streams were not idle: the demo event producer pushed batches
  throughout (18,012 frames delivered), but never a steady flow of thousands of reports
  a minute.
- The rig's database is on the same machine, so its queries are faster than Neon's.
  The round-trip time is simulated, not Neon's real latency.
- The hosted 10,000 streams were held for about 4 minutes, not hours; heartbeats
  every 25 s are meant to keep Render's proxy from closing them, but a long hold is untested.
- The new frontend has been typechecked but not run in a browser.
- Ingestion now costs about four database round trips per report (it was six: the
  knowledge base is cached, and the standing lookup shares a round trip with the
  corroboration lookup). Measured at 15 reports a second and an 80 ms round trip,
  the median fell from about 547 ms to about 383 ms, which is two round trips. Above
  roughly 40 a second the 0.1 CPU is the limit, so fewer round trips no longer help
  there. The simulated round trip is not Neon's real latency, so measure on the real region.

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

## Reading the server from the inside

`GET /v1/admin/metrics` (staff sign-in) reports what the server itself experienced, so a
slow load test can be explained instead of guessed at. Fetch it before and after a run
and compare:

| Field | What a climb means |
| --- | --- |
| `cpu.throttled_usec`, `cpu.throttled_periods` | the platform's CPU quota stopped the process: the limit is Render's CPU |
| `database_pool.acquire_wait_seconds`, `acquires_that_waited` | requests queued for a database connection: raise `DB_MAX_CONNS` or cut round trips |
| `requests.slow_over_1s` | the handler itself took over a second (time queued in front of the server is not counted) |
| `requests.shed_503` | the server turned work away on purpose |
| `memory.container_used_mib` against `container_limit_mib` | close to the limit means an out-of-memory kill is near |
| `streams.turned_away`, `streams.cut_loose` | streams refused for capacity, or dropped for being too slow |

If the client sees p95 of 58 s while these stay quiet, the delay is outside the process
(Render's proxy, the network, or the load generator).

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

1. Rerun the hosted 1,000-user test from a cloud machine near Render to settle whether
   the earlier failure was the test PC or Render's outbound limit.
2. Load fewer history rows on the first dashboard load (the app asks for 2,000) and
   fetch older rows on demand, to cut outbound bytes.
3. Measure delivery with 10,000 streams and a steady flow of thousands of reports a minute.
4. Measure ingestion on the real Neon region; the rig only simulates its round-trip time.
5. Click through the dashboard in a browser with the new live stream.
6. Cross-check Render's and Neon's bandwidth and transfer allowances.
