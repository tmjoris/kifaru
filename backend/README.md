# KIFARU Go API

KIFARU's backend is a Go `net/http` service backed exclusively by PostgreSQL.
The production database is hosted on Neon; SQLite is retained only as the
one-time source for the migration utility.

## Run locally

```bash
export DATABASE_URL='postgresql://...'
go run .
```

The service listens on `$PORT`, defaulting to `8000`.

`institutions` stores neutral institution identity and category. Reporting and
receiving are per-record relationships through `reports.reporting_institution`,
`reports.destination_institution`, and the corresponding alert fields. A bank is
never permanently assigned one of those roles. Startup seeds the licensed Kenyan
commercial bank directory and installs institution foreign keys for new records.

## API

```text
POST  /v1/reports
POST  /v1/reports/batch
POST  /v1/reports/csv
POST  /validate-csv
POST  /v1/hooks/{source}

GET   /v1/alerts?institution=
GET   /v1/reports?institution=
GET   /v1/history?institution=
GET   /v1/validations/{report_id}
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
```

## Existing-data migration

The migration tool copies the tracked demo database into PostgreSQL:

```bash
python -m pip install -r requirements.txt
export DATABASE_URL='postgresql://...'
python scripts/migrate_sqlite_to_postgres.py --reset
```

## Render

The repository-level `render.yaml` deploys:

- `kifaru-api`: this Go service
- `kifarulive`: the Vite static frontend

Set the API service's `DATABASE_URL` to the Neon pooled connection string.
