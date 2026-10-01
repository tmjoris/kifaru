# KIFARU Go API

KIFARU's backend is a Go `net/http` service backed exclusively by PostgreSQL.
The production database is hosted on Neon. The retired FastAPI, SQLite and
Python utility layer has been removed; Go owns authentication, routing,
validation, migrations, streaming and administration.

## Run locally

```bash
export DATABASE_URL='postgresql://...'
go run .
```

The service listens on `$PORT`, defaulting to `8000`.

Set `FRONTEND_ORIGIN` to the exact browser origin when the frontend and API are
deployed separately.

`institutions` stores neutral institution identity and category. Reporting and
receiving are per-record relationships through `reports.reporting_institution`,
`reports.destination_institution`, and the corresponding alert fields. A bank is
never permanently assigned one of those roles. Startup upserts every licensed
Kenyan bank from `data/kenyan_banks.json` (37 commercial banks and HFC) and the
two largest mobile money providers (M-Pesa and Airtel Money), keeping
any threshold an administrator has changed, and installs institution foreign
keys for new records. The codes `bank_a`, `bank_b`, `psp_c` and `sacco_d` are
kept for NCBA, KCB, Equity Bank and I&M Bank so that existing records still
resolve.

## Authentication and authorization

Startup seeds an institution demo account for each of the 38 licensed banks,
M-Pesa and Airtel Money, plus one Kifaru staff account. Passwords are stored only
as bcrypt hashes. Successful sign-in creates a random opaque token; PostgreSQL
stores its SHA-256 digest, CSRF token, user, timestamps and expiry.

The plaintext pitch credentials are maintained only in the repository root's
git-ignored `demo-credentials.txt`.

Institution sessions can access only their own history, reports, alerts,
configuration threshold and related validations. Only the receiving institution
can change an alert's state. Staff sessions can access ecosystem statistics,
audit history, global controls, manual revalidation and the synthetic stream.

Failed passwords increment an account counter. Five failures lock the account
for five minutes. Standard sessions expire after eight hours; **Keep me signed
in** sessions expire after seven days.

## API

```text
POST  /v1/auth/login
GET   /v1/auth/session
POST  /v1/auth/logout

POST  /v1/reports
POST  /v1/reports/batch
POST  /v1/reports/csv
POST  /validate-csv
POST  /v1/hooks/{source}

GET   /v1/alerts?institution=
GET   /v1/reports?institution=
GET   /v1/history?institution=
GET   /v1/validations/{report_id}
GET   /v1/stream?institution=
POST  /v1/alerts/{alert_id}/state
GET   /v1/standard
GET   /v1/stats
GET   /v1/institutions

GET   /v1/admin/config
PATCH /v1/admin/config
GET   /v1/admin/kb
POST  /v1/admin/kb
DELETE /v1/admin/kb
POST  /v1/admin/revalidate
GET   /v1/admin/audit
GET   /v1/admin/demo-stream
POST  /v1/admin/demo-stream/state
POST  /v1/admin/demo-stream/emit
POST  /v1/admin/demo-stream/reset
```

The demo-stream endpoints control a durable PostgreSQL-backed
`sentinel.security-alert` topic. It produces clearly marked synthetic Microsoft
Sentinel-shaped events every 30 seconds, records ordered offsets and processing
outcomes, retains the latest 500 processed events with their generated data, and
can remove all generated data through the reset endpoint. Paired events share a
protected destination artefact across two reporting institutions; the second
event automatically revalidates the first weak SIM-swap report.

## Schema migrations

The service embeds ordered SQL files from `migrations/`. Startup applies each
unseen migration in a PostgreSQL transaction and records the filename in
`schema_migrations`.

`003_synthetic_institutions.sql` retires obsolete directory placeholders.
`004_authentication.sql` adds demo users and expiring sessions. Startup seeds
the bank and staff pitch accounts only after the current institution directory
exists.
`005_rolling_demo_stream.sql` resumes streams stopped by the former event cap;
the Go producer then maintains rolling retention and repairs the same exact
legacy state if a retiring instance recreates it during a rolling deployment.

## Go package layout

All files use package `main`, separated by responsibility:

- `main.go` — process startup
- `models.go` — shared models and constants
- `database.go` — schema, migrations and institution seed data
- `auth.go` — password verification, sessions and authorization helpers
- `router.go` — CORS and HTTP routing
- `pipeline.go` — atomic validation and revalidation
- `handlers.go` — dashboard and administrative endpoints
- `events.go` — live events and synthetic Sentinel producer
- `csv_ingest.go` — CSV adapter
- `support.go` — shared database, JSON and parsing helpers

## Render

The repository-level `render.yaml` deploys:

- `kifaru-api`: this Go service
- `kifarulive`: the Vite static frontend

Set the API service's `DATABASE_URL` to the Neon pooled connection string and
`FRONTEND_ORIGIN` to the deployed frontend URL.

See the repository-level `README.md` for the complete processing, revalidation,
alert, audit, test and deployment documentation.
