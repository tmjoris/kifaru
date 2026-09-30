# KIFARU-MS-Sept-2026-Hackathon

Kifaru is a cross-institution fraud intelligence exchange. Fraud campaigns move
money across banks, PSPs and mobile-money wallets faster than any single
institution can see the whole picture. Kifaru lets an institution that detects
fraud publish a protected fingerprint - a hashed destination account, hashed
MSISDN, or shared device profile, never raw customer data - so every other
participating institution can recognise the same pattern before the money
moves further: **Detect -> Fingerprint -> Share -> Match -> Act**.

## Run the React app

From the repository root:

```bash
npm install
npm run dev
```

Open `http://127.0.0.1:5173`. In another terminal, start the validation API:

```bash
export DATABASE_URL='postgresql://...'
npm run server
```

Vite forwards `/api/*` requests to the backend at `http://127.0.0.1:8000`.
The institution sign-in is at `/`; the separate Kifaru operations sign-in is at
`/staff`. A bank signs into one institution workspace containing submitted flags,
received alerts, related history, reports, knowledge, and governance. Reporting
and receiving are roles on each transaction, not permanent bank types.

The React app includes the CBK-licensed commercial bank directory, search/outcome
filters, reports and risk-code drilldowns, read-only investigations,
knowledge-base content, admin settings, theme switching, and the collapsible sidebar.
Use `?clawpilotTheme=dark` or `?clawpilotTheme=light` to select the initial theme;
otherwise it follows the system preference.

### Exchange, pipeline, and campaign chain

- **Exchange tab** - an ecosystem-wide feed of protected fraud fingerprints
  (masked destination hashes) published by every institution, sorted by how
  many other institutions have independently matched each one. This is the
  cross-institution visibility layer described in the KIFARU problem statement:
  institutions never see each other's raw customer data, only the shared
  fingerprint, its pattern, and a match count.
- **Pipeline** - every transaction investigation shows where the record sits in
  Detect -> Fingerprint -> Share -> Match -> Act, driven by real backend
  corroboration data (`corroboration_count`, `corroborating_institutions`).
- **Fraud chain** - the investigation drawer visualizes the reporting bank,
  the shared fingerprint, the receiving bank, and the outcome (held, released,
  or under review), making a multi-institution campaign visible in one view.

### Prototype boundaries

CSV uploads call the real prototype API. The sign-in form is demo gating rather
than authentication, and bank filtering is client-side, not an authorization boundary. Connector
inventory is illustrative and does not contact external systems. Institution
threshold changes are persisted by the backend.
Backend records and institution thresholds persist in Neon PostgreSQL and reload on refresh.
Production use requires server-side authentication, tenant authorization,
and real action endpoints.

### Build and checks

```bash
npm run build
npm test
cd backend && go test ./...
npm run preview
```

`npm run build` type-checks the app and produces `dist/`. Local preview proxies
`/api` to the separately running backend. Production reads the Go API hostname
from `VITE_API_URL`. Node 22.18+ (22.x) or Node 24+ is required.

## Contents

- `src/` - React + TypeScript components, state, API client, reference data, and styles.
- `fraud-soc-dashboard-prototype.html` - original interactive prototype, kept for reference.
- `backend/` - working backend API prototype for the Kifaru validation agent.
- `fraud-validation-agent-skill.md` - shareable AI-agent skill instructions.
- `kifaru-architecture.excalidraw` / `kifaru-architecture.png` - architecture diagram.

## Run the backend

```bash
export DATABASE_URL='postgresql://...'
npm run server
```

Then open `http://127.0.0.1:8000/health`.
The backend's usage, migration, and API documentation is in
`backend/README.md`. The `/v1/history` endpoint now also returns each
report's `destination_account_hash`, `destination_msisdn_hash`,
`corroborating_institutions`, and `corroboration_count`, which the dashboard
uses to power the Exchange, Pipeline, and fraud-chain views.

## Deploy on Render

`render.yaml` provisions a Go web service and a Vite static site. Set
`DATABASE_URL` on `kifaru-api` to the Neon pooled PostgreSQL connection string;
the frontend receives the API hostname from the Blueprint automatically.