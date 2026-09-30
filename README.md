# Kifaru

Kifaru is a shared fraud-validation platform for Kenyan financial institutions.
It receives fraud signals that banks, SACCOs and payment providers have already
detected, checks them against a common risk standard, looks for corroboration
from other institutions and alerts the institution that can still stop the
money.

Kifaru does not replace an institution's fraud system and does not block
payments. It gives the receiving institution timely, explainable evidence so
its own staff can decide what to do.

**Live application:** https://kifarulive.onrender.com

**Kifaru staff route:** https://kifarulive.onrender.com/staff

**API health:** https://kifaru-api.onrender.com/health

## Project status

The application is a deployed school-project prototype built with synthetic
data. The core validation flow, PostgreSQL persistence, institution dashboards,
cross-institution matching, automatic revalidation, alert lifecycle and
continuous integration are implemented.

The sign-in forms are demonstration gates. They do not provide production
authentication or tenant authorization. Microsoft Entra ID is deliberately
outside this prototype's scope.

## What the system does

1. An institution submits a suspected fraud report through REST, webhook,
   SOC-style connector, batch JSON or CSV.
2. Kifaru rejects clear identifiers and reports that contain no recognised
   risk code.
3. Institution rule IDs and behavioural evidence are mapped to the central
   Kifaru fraud standard.
4. A deterministic validator scores the report using risk severity,
   corroboration, knowledge-base matches and the institution's threshold.
5. The report receives one outcome:
   `VALIDATED_FRAUD`, `INSUFFICIENT_EVIDENCE` or `NOT_FRAUD`.
6. Only validated fraud produces an alert for the destination institution.
7. A new matching report automatically causes earlier weak reports to be
   evaluated again.
8. Connected dashboards receive new alerts through Server-Sent Events.
9. Analysts can acknowledge, action or dispute alerts. A dispute requires a
   comment and creates a notification for the reporting institution.

## Users and workspaces

### Institution staff

Institution staff sign in at `/`. A bank is not permanently classified as a
"reporting bank" or a "receiving bank". Those are roles in a particular report.
Every institution workspace therefore contains:

- Flags submitted
- Alerts received
- Related history
- Reports and risk-code analysis
- Knowledge-base information
- Institution governance and threshold settings

The institution selector contains the current Kenyan commercial-bank directory,
along with the prototype's existing SACCO and payment-provider entries.
Dense transaction tables prioritise decision fields on briefing-sized desktop
screens and switch to labelled record cards on mobile. Full provenance and
customer-reference details remain available in each investigation drawer.

### Kifaru staff

Kifaru staff use `/staff`. This route does not ask for an institution. It opens
the ecosystem view of shared fingerprints, cross-institution matches and
participating institutions.

## The visibility gap Kifaru closes

One institution can identify a compromised customer while every downstream
participant sees only a normal-looking transfer. Kifaru connects those partial
views without moving raw customer identifiers outside each institution.

```mermaid
flowchart LR
    Customer["Compromised customer"]
    BankA["Bank A<br/>detects account takeover"]
    BankB["Bank B<br/>receives the transfer"]
    Wallet["Payment provider<br/>sees a wallet credit"]
    Cashout["Cash-out point"]

    BankA -.->|"protected fraud signal"| Kifaru["Kifaru<br/>shared validation"]
    BankB -.->|"matching protected artefact"| Kifaru
    Kifaru ==>|"corroborated alert"| BankB

    Customer --> BankA
    BankA -->|"transfer"| BankB
    BankB -->|"forward"| Wallet
    Wallet -->|"withdrawal"| Cashout
```

The transaction still moves through institution-owned systems. Kifaru adds the
shared evidence needed for the receiving institution to recognise the wider
campaign and decide whether to hold or investigate the funds.

## System context

```mermaid
flowchart LR
    FraudSystem["Institution fraud system"]
    Analyst["Institution analyst"]
    KifaruStaff["Kifaru staff"]
    Kifaru["Kifaru platform"]
    Database[("Neon PostgreSQL")]
    BankSystem["Institution payment system"]

    FraudSystem -->|"Hashed fraud report"| Kifaru
    Analyst -->|"Review alerts and record decisions"| Kifaru
    KifaruStaff -->|"Monitor shared signals"| Kifaru
    Kifaru -->|"Reports, validations, alerts and audit"| Database
    Kifaru -->|"Advisory or hold recommendation"| Analyst
    Analyst -->|"Human decision outside Kifaru"| BankSystem
```

Customer names, raw account numbers, raw phone numbers and hashing secrets stay
inside the participating institution. Kifaru receives protected identifiers,
amounts, institution codes, rule IDs and supporting evidence.

## Deployed architecture

The original design proposed FastAPI. The implementation uses Go instead. The
framework changed, but the important design properties remain: one stateless API,
one PostgreSQL database, deterministic scoring and a React dashboard.

```mermaid
flowchart TB
    Browser["Browser\nReact 19 + TypeScript"]
    Static["Render static service\nkifarulive"]
    API["Render Go web service\nkifaru-api"]
    Neon[("Neon PostgreSQL")]
    GitHub["GitHub repository"]
    Actions["GitHub Actions\nNode + Go + PostgreSQL"]

    GitHub -->|"Auto-deploy main"| Static
    GitHub -->|"Auto-deploy main"| API
    GitHub --> Actions
    Browser --> Static
    Browser -->|"HTTPS JSON + SSE"| API
    API -->|"Pooled PostgreSQL connection"| Neon
```

The frontend and API are deployed separately. `VITE_API_URL` supplies the API
hostname during the frontend build. Render provides HTTPS for both services.

## Backend processing pipeline

The core write path runs inside one PostgreSQL transaction. A report,
validation, alert, artefacts, knowledge-base update and audit records either
commit together or roll back together.

```mermaid
sequenceDiagram
    participant I as Institution system
    participant A as Go API
    participant D as PostgreSQL
    participant R as Receiving dashboard

    I->>A: Submit protected fraud report
    A->>A: Validate contract and hash boundary
    A->>D: Begin transaction
    A->>D: Check institution and retry key
    A->>A: Normalise risk codes
    A->>D: Find corroborating artefacts
    A->>A: Compute deterministic score
    A->>D: Store report and indexed artefacts
    A->>D: Store current validation and audit record
    alt Validated fraud
        A->>D: Store alert and initial alert action
        A->>D: Add destination hash to known-bad list
    end
    A->>D: Revalidate earlier matching weak reports
    A->>D: Commit transaction
    A-->>I: Report ID, status, score and reasons
    A-->>R: Server-Sent Event for committed alert
```

### Retry safety

`(reporting_institution, transaction_ref)` is unique. Repeating a submission
returns the original report rather than inserting a duplicate. The first
pipeline migration removes duplicate replay data before installing this
constraint.

### Automatic revalidation

Artefacts are indexed by protected hash and observation time. Matching is
limited to reports from other institutions within the 30-day prototype window.

```mermaid
flowchart TD
    New["New report stores artefact"]
    Match{"Same protected hash from\nanother institution?"}
    Prior["Find current weak validations"]
    Score["Score each prior report again"]
    Changed{"Now validated fraud?"}
    Replace["Mark old validation historical\nand store replacement"]
    Alert["Create receiving-institution alert"]
    History["Keep replacement result in history"]

    New --> Match
    Match -- No --> History
    Match -- Yes --> Prior --> Score --> Replace --> Changed
    Changed -- Yes --> Alert
    Changed -- No --> History
```

Every replacement validation points to the validation it supersedes. Only one
validation per report is marked current.

## Validation model

Scoring is deterministic and implemented with named constants in the Go API:

| Input | Weight |
|---|---:|
| Risk-code severity | `severity × 0.50` |
| Each corroborating institution | `+0.18`, capped at three |
| Known-bad match | `+0.30` |
| Known-good match | `-0.55` |
| Institution score above its threshold | `+0.10` |

The resulting score is clamped to `0.00–0.99`.

| Score | Outcome |
|---|---|
| `>= 0.60` | `VALIDATED_FRAUD` |
| `>= 0.35` and `< 0.60` | `INSUFFICIENT_EVIDENCE` |
| `< 0.35` | `NOT_FRAUD` |

The validation stores the reason codes, corroborating institutions, agent
version, decision time, configuration version and plain-language explanation.
The explanation describes the decision but never changes the score.

## Alert lifecycle

Alerts carrying a transferable amount request a hold. Events with no
transferable amount are marked advisory.

```mermaid
stateDiagram-v2
    [*] --> Sent
    Sent --> Acknowledged
    Sent --> Disputed: Comment required
    Acknowledged --> Actioned
    Acknowledged --> Disputed: Comment required
    Actioned --> [*]
    Disputed --> [*]
```

Every transition creates an `alert_actions` row and an audit event. A dispute
also creates a notification addressed to the reporting institution.

## Data model

```mermaid
erDiagram
    INSTITUTIONS ||--o{ REPORTS : submits
    INSTITUTIONS ||--o{ ALERTS : receives
    REPORTS ||--o{ ARTEFACTS : contains
    REPORTS ||--o{ VALIDATIONS : evaluated_by
    VALIDATIONS ||--o| ALERTS : may_create
    ALERTS ||--o{ ALERT_ACTIONS : records
    INSTITUTIONS ||--o{ NOTIFICATIONS : receives
    DEMO_STREAM_STATE ||--o{ DEMO_EVENTS : allocates
    DEMO_EVENTS }o--o| REPORTS : generates

    INSTITUTIONS {
        text code PK
        text name
        text type
        real threshold
        integer active
    }
    REPORTS {
        text report_id PK
        text reporting_institution FK
        text destination_institution FK
        text transaction_ref
        text submitted_at
        text risk_codes
        text evidence
    }
    ARTEFACTS {
        text report_id FK
        text institution_code FK
        text artefact_type
        text artefact_hash
        text observed_at
    }
    VALIDATIONS {
        text validation_id PK
        text report_id FK
        real validation_score
        text status
        integer configuration_version
        text supersedes_validation_id
        integer is_current
    }
    ALERTS {
        text alert_id PK
        text validation_id
        text receiving_institution FK
        text reporting_institution FK
        text alert_type
        text state
    }
    ALERT_ACTIONS {
        bigint id PK
        text alert_id FK
        text action
        text comment
        text actor
        text at
    }
    NOTIFICATIONS {
        bigint id PK
        text institution_code FK
        text event_type
        text record_id
        text payload
    }
    DEMO_STREAM_STATE {
        boolean singleton PK
        boolean enabled
        integer cadence_seconds
        bigint next_offset
        integer emitted_since_reset
        timestamp last_emitted_at
    }
    DEMO_EVENTS {
        bigint event_offset PK
        text topic
        text partition_key
        jsonb payload
        text status
        text report_id FK
        text outcome
        timestamp processed_at
    }
```

## Database migrations and audit integrity

SQL migrations live in `backend/migrations/` and are embedded into the Go
binary. Applied filenames are recorded in `schema_migrations`, so each migration
runs once.

The first migration:

- Removes duplicate replay submissions while retaining one original report
- Adds retry-safe report uniqueness
- Creates and backfills the artefact index
- Adds validation replacement and configuration-version fields
- Adds advisory alert classification
- Adds alert actions and institution notifications
- Extends audit records with old value, new value and reason
- Installs a PostgreSQL trigger that rejects audit-row updates and deletes

The second migration creates the synthetic SOC event broker:

- One durable producer-state row with pause/resume and a 30-second cadence
- An ordered `sentinel.security-alert` event log with monotonic offsets
- Processing status, report IDs, outcomes and failure details for every event
- A 500-event safety cap between resets

Audit entries are written for validations, automatic and manual revalidation,
alert decisions, configuration updates, threshold changes and knowledge-base
changes.

## Synthetic Microsoft Sentinel event stream

Kifaru staff can run a continuous synthetic SOC feed from the `/staff`
workspace. It behaves like a small Kafka topic while remaining deployable on the
project's existing Render and PostgreSQL services:

- Topic: `sentinel.security-alert`
- Default cadence: one event every 30 seconds
- Durable, increasing offsets
- Pause, resume, emit-one and reset controls
- Retained processing result for each event
- Server-Sent Event notification after processing
- Automatic pause after 500 events until staff reset the stream

The payloads follow public Microsoft Sentinel `SecurityAlert` conventions,
including `SystemAlertId`, `AlertName`, `AlertSeverity`, `ProviderName`,
`CompromisedEntity`, `Entities`, `Tactics`, `Techniques` and
`ExtendedProperties`. Transaction amounts and behavior are synthetic and
informed by the public PaySim mobile-money simulator. No private SOC logs or
customer transactions are copied into the repository.

The producer rotates through paired cross-institution scenarios:

- SIM change followed by a new-device transfer
- New mule account receiving and rapidly forwarding funds
- New beneficiary followed by a high-value transfer
- Credential reset from an unfamiliar device
- An unusual but potentially legitimate payment requiring corroboration

Two consecutive events in a campaign share one protected destination artefact
but originate from different institutions. The first SIM-swap report remains
`INSUFFICIENT_EVIDENCE`; the matching report from another institution upgrades
it to `VALIDATED_FRAUD`. This exercises Kifaru's automatic corroboration and
revalidation path rather than merely changing dashboard counters.

```mermaid
sequenceDiagram
    actor Staff as Kifaru staff
    participant UI as Staff event console
    participant Producer as Go background producer
    participant Broker as PostgreSQL event log
    participant Pipeline as Fraud validation pipeline
    participant Stream as SSE subscribers

    Staff->>UI: Start stream
    UI->>Producer: Enable 30-second cadence
    loop Every 30 seconds, up to 500 events
        Producer->>Broker: Lock state and claim next offset
        Producer->>Broker: Append pending Sentinel-shaped event
        Producer->>Pipeline: Submit synthetic protected report
        Pipeline->>Broker: Commit report, validation and optional alert
        Producer->>Broker: Record outcome against event offset
        Producer-->>Stream: Publish processed demo-event
        Stream-->>UI: Refresh broker and ecosystem views
    end
    Staff->>UI: Pause or reset
    UI->>Broker: Pause producer and remove synthetic records
    Note over Broker: Immutable audit history remains
```

Reset removes the stream's reports, validations, alerts, indexed artefacts,
knowledge-base additions and broker events. Append-only audit records remain.

Public references used for the synthetic schema and scenarios:

- [Microsoft Sentinel security alert schema](https://learn.microsoft.com/en-us/azure/sentinel/security-alert-schema)
- [SecurityIncident table reference](https://learn.microsoft.com/en-us/azure/azure-monitor/reference/tables/securityincident)
- [CommonSecurityLog table reference](https://learn.microsoft.com/en-us/azure/azure-monitor/reference/tables/commonsecuritylog)
- [Microsoft Sentinel public sample data](https://github.com/Azure/Azure-Sentinel/tree/master/Sample%20Data)
- [PaySim mobile-money simulator](https://github.com/EdgarLopezPhD/PaySim)

## Real-time delivery

`GET /v1/stream?institution=...` opens a Server-Sent Events connection.
Institution workspaces subscribe to their own code; the Kifaru staff workspace
subscribes to the ecosystem stream. The browser reconnects automatically if the
connection drops, and committed alert events trigger a dashboard refresh.

Streams are held in memory because the prototype runs one API instance. A
multi-instance deployment would need PostgreSQL `LISTEN/NOTIFY` or another
shared delivery mechanism.

## Main API routes

```text
POST   /v1/reports
POST   /v1/reports/batch
POST   /v1/reports/csv
POST   /validate-csv
POST   /v1/hooks/{source}

GET    /v1/history?institution=
GET    /v1/reports?institution=
GET    /v1/alerts?institution=
GET    /v1/validations/{report_id}
GET    /v1/stream?institution=
POST   /v1/alerts/{alert_id}/state

GET    /v1/institutions
GET    /v1/standard
GET    /v1/stats

GET    /v1/admin/config
PATCH  /v1/admin/config
GET    /v1/admin/kb
POST   /v1/admin/kb
DELETE /v1/admin/kb
POST   /v1/admin/revalidate
GET    /v1/admin/audit
GET    /v1/admin/demo-stream
POST   /v1/admin/demo-stream/state
POST   /v1/admin/demo-stream/emit
POST   /v1/admin/demo-stream/reset
```

## Requirements coverage

The implementation is based on:

- `Kifaru Requirements Specification`, Part I, 25 September 2026
- `Kifaru System Design and Planning`, Part II, 26 September 2026

### Implemented

- Four ingestion paths
- Contract validation and protected-identifier gate
- Unique report IDs and recorded submission channel/time
- Central risk-code mapping and behavioural derivation
- Rejection when no risk code can be produced
- Deterministic three-outcome validation
- Stored reasons, corroboration, agent version and timing
- Automatic revalidation after cross-institution corroboration
- Template explanations independent of the score
- Destination-only routing for validated fraud
- No alerts for weak or rejected outcomes
- Real-time dashboard alert events
- Advisory alerts for zero-amount events
- Acknowledge, action and dispute lifecycle
- Mandatory dispute comments and reporting-institution notifications
- Unified institution views for received alerts, submitted flags and history
- Search and outcome filtering
- Ecosystem-wide Kifaru staff view
- Global and per-institution threshold configuration
- Ingestion-source configuration
- Known-good and known-bad knowledge base
- Automatic known-bad addition after validated fraud
- Manual revalidation
- Expanded append-only audit history
- Durable Microsoft Sentinel-shaped synthetic event stream
- Ordered event offsets, processing outcomes, pause/resume and reset controls
- No payment blocking or reversal API

### Prototype limitations

- Sign-in is demo gating, not production authentication.
- Institution filtering is not yet a server-side authorization boundary.
- Microsoft Entra ID is not planned for this school prototype.
- Synthetic identifiers use the historical prototype formats; the API rejects
  missing hash prefixes but does not yet require a full 64-character digest for
  every legacy evidence field.
- SSE delivery is designed for one API instance.
- The demo broker uses PostgreSQL rather than an external Kafka cluster. It
  preserves the ordering, offset, retention and consumer-facing behavior needed
  for this prototype without adding another hosted service.
- All continuous-stream records are synthetic. Public Sentinel schemas and
  PaySim patterns inform their shape; they are not real bank SOC events.
- The API does not yet publish an OpenAPI document.
- Accuracy and latency figures still need a final measured report from the
  complete synthetic replay.
- The frontend and API are separate Render services rather than one origin.
- Azure OpenAI remains optional and disabled; explanations use deterministic
  templates.

## Repository layout

```text
.
├── .github/workflows/ci.yml      # Frontend and PostgreSQL-backed backend CI
├── backend/
│   ├── data/                     # Fraud standard and synthetic datasets
│   ├── migrations/               # Ordered PostgreSQL migrations
│   ├── scripts/                  # Generation, replay and migration utilities
│   ├── main.go                   # Go HTTP API and validation pipeline
│   ├── main_test.go              # Unit and PostgreSQL integration tests
│   └── schema.sql                # Baseline PostgreSQL schema
├── public/                       # Static frontend assets
├── src/                          # React application and API client
├── render.yaml                   # Render API and frontend services
└── README.md                     # Project and architecture documentation
```

## Local development

### Requirements

- Node.js 22.18 or later
- Go 1.22 or later
- PostgreSQL, or a Neon PostgreSQL connection string

### Start the API

```bash
export DATABASE_URL='postgresql://...'
npm run server
```

The API listens on `http://127.0.0.1:8000` unless `PORT` is set.

### Start the frontend

```bash
npm install
npm run dev
```

Open `http://127.0.0.1:5173`. Vite proxies `/api` to the local Go API.
The Kifaru staff route is `http://127.0.0.1:5173/staff`.

## Tests

Run the local checks:

```bash
npm run build
npm test
cd backend
go test ./...
```

Set `TEST_DATABASE_URL` to run the PostgreSQL integration test locally:

```bash
export TEST_DATABASE_URL='postgresql://.../kifaru_test'
go test ./...
```

The backend tests cover:

- Clear identifier rejection
- Behavioural risk-code derivation
- Advisory versus hold alert classification
- Atomic PostgreSQL processing
- Automatic revalidation after corroboration
- Current and superseded validation records
- Retry idempotency
- Mandatory dispute comments
- Reporting-institution notification creation
- Rollback when persistence fails
- Audit-record creation
- Sentinel-shaped synthetic event generation
- Cross-institution campaign pairing
- Durable event retention and report processing
- Stream reset cleanup

GitHub Actions starts PostgreSQL 16, builds and tests the frontend, and runs the
Go test suite on every push to `main` and every pull request.

## Deployment

`render.yaml` defines:

- `kifaru-api`: Go web service
- `kifarulive`: Vite static frontend

The API requires `DATABASE_URL`. The frontend receives `VITE_API_URL` from the
API service. Render deploys both services automatically after commits reach
`main`.

The static build also writes `dist/staff/index.html`, ensuring `/staff` works
even if a host does not apply single-page-application rewrites.

## Privacy and operating boundaries

- Raw customer identifiers must not be submitted.
- Kifaru stores protected artefacts and institution routing data.
- Request bodies are not written to application logs.
- Kifaru validates and alerts; it never executes a payment hold.
- Real deployment would require authenticated users, authenticated institution
  systems, server-side tenant authorization, a hashing-key agreement and a data
  protection impact assessment.
- The current data is synthetic and must not be treated as evidence of
  production accuracy.
