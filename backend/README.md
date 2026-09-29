# KIFARU — shared fraud validation platform (POC backend)

Banks detect. KIFARU validates. Only validated fraud reaches a receiving institution.

```
ingest → normalize → validate (agent) → route or suppress → persist → stream
```

## Run it

```bash
python3 -m venv .venv
.venv/bin/pip install -r requirements.txt
uvicorn app.main:app --reload            # http://127.0.0.1:8000

python scripts/seed_kb.py                # known-good suppression list
python scripts/replay.py                 # feed 91 reports through all 4 channels
python scripts/score.py                  # prove it against ground truth
```

Open `http://127.0.0.1:8000/` for the shared-platform web app, `/docs` for the API.

Azure OpenAI is optional. Without a key the agent uses a deterministic explanation fallback, so the demo never depends on a live network call.

## Proven output

Replaying the dataset through the live API:

```
submitted   91     accepted 91     rejected 0     errors 0
  VALIDATED_FRAUD          16   true 15  false 1   precision 94%
  NOT_FRAUD                48   correct 38  missed 10
  INSUFFICIENT_EVIDENCE    27
alerts routed               8   all to psp_c, from bank_b and sacco_d
channels        soc_connector 24 · rest 29 · webhook 22 · batch 16
agent latency   1 ms average
```

**38 reports were suppressed at the centre and never reached a receiving institution.** That is the product.

The highest-scoring routed alert is KES 96,500 from `bank_b` to `psp_c` at score 0.99 — the CAMP-003 takeover, whose receiving-side transaction its own engine scored 0.20 APPROVE. The platform found and routed it without being told.

## What `scripts/score.py` proves

| | |
|---|---|
| Analysis quality | Confusion matrix read straight from the platform DB against held-out ground truth |
| Privacy gate | A cleartext account identifier is rejected with HTTP 422 at the schema boundary |
| Substance gate | A report producing no risk codes is rejected — nothing to validate |
| Admin control is real | Raising the threshold 0.60 → 0.80 flips the false positive to INSUFFICIENT_EVIDENCE, score unchanged |
| Routing | Alerts reach only the destination institution named in the report |
| Agent learning | `known_bad` entries are written by `added_by=agent`, not by hand |
| Audit | Every KB write and config change is attributable |

## Architecture

```
app/
  schemas.py        Report · Validation · Alert · RiskCode   ← the frozen contract
  store.py          SQLite; every query is plain SQL, swap for Postgres/Azure Table
  normalize.py      bank rule ids + evidence → central fraud standard codes
  agent/validator.py  the deterministic scorer + LLM explanation  ← the product
  main.py           four ingest channels, platform reads, admin, SSE
ui/index.html       shared platform web app: alerts / flags / history / admin
scripts/            generate · derive · seed · replay · score · validate
data/               seed CSV/JSON + kifaru.db (created on first run)
```

### The agent

| | |
|---|---|
| Input | One normalised report: risk codes, hashed artefacts, evidence, bank score and threshold |
| Reasoning | Deterministic weighted sum — risk-code severity × 0.50, +0.18 per corroborating institution (cap 3), +0.30 known-bad, **−0.55 known-good suppression**, +0.10 above the bank's own threshold |
| Tools | Prior-report index (hash matching), knowledge base, the standard |
| Output | Score, status ∈ {VALIDATED_FRAUD ≥0.60, INSUFFICIENT_EVIDENCE ≥0.35, NOT_FRAUD}, reason codes, corroborating institutions |
| LLM role | Explanation only, after the decision. Never scores, never decides, never routes |
| Human action | Receiving analyst holds for step-up verification. KIFARU never blocks |

Every weight is a named constant at the top of `validator.py`. Every decision traces to a reason code. A regulator can audit any alert the platform ever sent.

## Privacy

`schemas.py` rejects any identifier not prefixed `sha256:`. KIFARU matches on salted hashes and never holds a customer identity. The only cleartext it stores is the destination **institution**, because it has to know who to alert — a routing label, not a customer identifier.

## API

```
POST  /v1/reports              /v1/reports/batch       /v1/hooks/{source}
POST  /v1/reports/csv          /validate-csv            CSV upload compatibility
GET   /v1/alerts?institution=  /v1/reports?institution=  /v1/history?institution=
GET   /v1/validations/{report_id}   /v1/standard   /v1/stats   /v1/institutions
GET   /v1/stream/{institution}                     server-sent events
GET   /v1/admin/config   PATCH /v1/admin/config
GET   /v1/admin/kb  POST /v1/admin/kb  DELETE /v1/admin/kb
POST  /v1/admin/revalidate   GET /v1/admin/audit
```

## Known gaps

- **Three alerts carry `amount = 0`** because the source events are logins and beneficiary additions, not transfers. There is no transaction to hold. Either filter to value-bearing events before routing, or add an `advisory` alert class distinct from `hold`.
- **Ten false negatives**, all first-reports with nothing to corroborate. The honest cost of requiring corroboration. `INSUFFICIENT_EVIDENCE` exists so these can be upgraded when a second institution reports — but automatic re-evaluation on new corroboration is not wired yet. It is roughly twenty lines in `process()`.
- SQLite and single-process in-memory SSE subscribers. Fine for a POC, not for concurrent institutions.
- No authentication. Add Entra app registrations with one app role per institution before any pilot.

## Prototype artifacts

- `fraud-soc-dashboard-prototype.html` — standalone dashboard prototype; CSV upload now posts to `http://127.0.0.1:8000/validate-csv`.
- `fraud-validation-agent-skill.md` — shareable skill spec for the fraud validation agent.
- `kifaru-architecture.excalidraw` and `kifaru-architecture.png` — editable and exported architecture diagram.
