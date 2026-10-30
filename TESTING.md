# Testing

What is tested, how to run it on Windows and Ubuntu, what the checks on a pull
request mean, and what is not covered yet. The expected results the tests pin
down (scores, balances, the director replay) come from
[TestWork.md](TestWork.md).

## The test kinds at a glance

| Kind | What it answers | Where | Count | Time |
| --- | --- | --- | --- | --- |
| **Unit and regression** | Do the rules still give the answers we agreed? | `demo-banks/*_test.go`, tests named `TestRegression_…` | 39 | 11 s |
| **Smoke** | Does it start, and do the main journeys work at all? | `TestSmoke_…`, `scripts/smoke.mjs` | 5 + live script | 1 s |
| **Security** | Can someone read, change or break what they should not? | `TestSecurity_…`, `demo-banks/web/common.test.mjs`, `npm audit`, `govulncheck` | 14 + 7 | 2 s |
| **Load and performance** | Does it stay correct and fast with many users? | `TestLoad_…`, `BenchmarkScore`, `load/k6-banks.js` | 3 + k6 | 3 s |
| **Integration** (existing) | Does the Kifaru API work against a real PostgreSQL? | `backend/*_test.go` (`TestPostgres…`) | 4 | needs a database |
| **Unit** (existing) | Kifaru's validation, policy and the web app's logic | `backend/*_test.go`, `src/domain.test.ts` | 16 + 11 | under 1 s |

The 61 Go tests in `demo-banks` expand to 133 checks with their sub-cases.
None of them needs a database or Kifaru, because the banks keep their state in
memory and the tests start the real banks, switch and director in the same
process on free ports.

## Run everything

Install the tools first (see **Start from scratch** in
[DEMO-GUIDE.md](DEMO-GUIDE.md)), then from the repository root.

Ubuntu:

```bash
npm ci
npm test                              # web app and page-escaping tests
(cd demo-banks && go test -race ./...)   # all demo-bank tests, about 30 seconds
```

Windows (PowerShell):

```powershell
npm ci
npm test
cd demo-banks; go test -race ./...; cd ..
```

`-race` turns on Go's data-race detector. On Windows it needs a C compiler
(install MinGW-w64 or MSYS2). If you do not have one, leave `-race` out.

Run one kind at a time with `-run`:

```
cd demo-banks
go test -v -run '^TestRegression' ./...
go test -v -run '^TestSmoke' ./...
go test -v -run '^TestSecurity' ./...
go test -v -run '^TestLoad' ./...
go test -run '^$' -bench BenchmarkScore ./...
```

On Windows, quote the pattern with double quotes if PowerShell complains:
`go test -v -run "^TestRegression" ./...`.

## What each kind covers

### Regression (and unit)

The score table in [TestWork.md](TestWork.md) is checked row by row: the base
score, each signal and its boundary (KES 50,000, the one-hour windows), every
Kifaru tier, and the 0.99 cap. Then the behaviour built on it:

- **Block and report lines.** NCBA 0.80 / 0.70, KCB 0.75 / 0.45, Equity 0.80 /
  0.70. A score exactly on the block line is blocked.
- **Repeat payment and lockout.** The second payment to a new beneficiary is
  blocked from a new device and allowed from a known one. A block ends every
  session on that device, refuses its sign-ins, spares other devices, and a
  reset clears it.
- **Fraud desk.** A block confirms the earlier payment to the same beneficiary
  and emails the receiving bank's mailbox.
- **Ledger.** Amount checks, held accounts, withdrawals, frozen-on-arrival
  credits, returns that refund exactly once, and a failed switch that gives the
  money back.
- **Switch.** Unknown bank, missing reference, a repeated message credited once
  (also when 30 arrive at the same time), and a failed delivery that can be
  retried.
- **Sessions.** Idle expiry, page refreshes not extending a session, the
  configurable limit.
- **Kifaru connector.** Connector off sends nothing. Flagged answers raise the
  score by the right amount. A Kifaru error or outage fails open. An expired
  Kifaru session is renewed. Reports go out only above the report line.
- **Hashing.** The Go banks and the web app produce the same digests for the
  same input.
- **Director.** Modes, steps, a shared run key after a reset, and the whole
  scripted attack without Kifaru: every step checks Grace, Peter, Faith and
  Kiprono's balances, ending at 327,500 / 268,000 / 380,000 / 0.

### Smoke

In-process: every customer, operations and director page returns HTML with its
`#app` root, and every stylesheet and script a page names, including the
modules those scripts import, exists and has the right content type. Then the
config endpoints, a sign-in, a payment between banks, the director's first step
and the operations actions.

Against the live demo, with the banks running:

```
npm run smoke                 # banks and director
npm run smoke -- --kifaru     # also the Kifaru API and web app
```

It signs in once per bank from a device named `smoke-check` and makes no
payments, so it is safe in the middle of a rehearsal. It exits with 1 if
anything fails. Set `NCBA_URL`, `KCB_URL`, `EQUITY_URL`, `NETWORK_URL`,
`KIFARU_API_URL` or `KIFARU_WEB_URL` for other ports.

### Security

- Every customer route refuses a missing, garbage, guessed or injected token.
  Tokens are 128 random bits, never repeated across 200 sign-ins, die on
  sign-out, and do not work at another bank.
- A customer cannot see or spend another customer's money.
- Sign-in gives the same answer for an unknown user and a wrong password, and
  shrugs off injection strings and a 100 KB username.
- No reply contains a customer password.
- Hostile bodies (`null`, arrays, nesting 20,000 deep, 2 MB strings, wrong
  types) sent to every POST route never cause a 5xx or change a balance.
  Bodies over 1 MB are refused.
- Credits and returns of zero or negative amounts are refused.
- Markup in a payee name stays inert JSON and is never returned as HTML.
- `/static/` cannot be used to read source files (`../`, encoded, double
  encoded).
- Only hashed identifiers leave a bank: reports and risk checks to Kifaru
  contain no account number, name or device ID.
- Every service listens on `127.0.0.1` only.
- In the browser code: `escapeHtml` leaves no `<`, `>` or quote characters, and
  the page header escapes a hostile bank name.
- Dependencies: `npm audit --audit-level=high` blocks a pull request. The
  `govulncheck` step reports Go findings without blocking (see below).

### Load and performance

`TestLoad_…` runs inside the Go test run, with loose limits so a busy build
machine does not cause false alarms:

- **Concurrent customers.** 40 customers (10 per account) each sign in, then
  25 times read their account and send a payment, across all three banks and
  the switch at once. Checks: no errors, every payment completes, p95 under 750
  ms, and **the total money in the system is exactly what it was before**.
- **Open dashboards.** 40 viewers poll the director and ops pages 30 times each.
  p95 must stay under 500 ms.
- **Rapid clicking** on **Next attacker step**: steps must run one at a time and
  the director's step count must match the clicks that succeeded.
- `BenchmarkScore` measures the fraud score (about 450 ns per call).

For a heavier run against the real running demo, use k6
(<https://grafana.com/docs/k6/latest/set-up/install-k6/>):

```
k6 run load/k6-banks.js
k6 run -e VUS=40 -e DURATION=60s load/k6-banks.js
```

**It resets the demo first**, so use it on a throwaway instance, or pass
`-e RESET=0`. A typical local result at 20 virtual customers: 0% failed
requests, p95 about 70 ms for payments, about 75 requests per second.

## What the checks on a pull request mean

Opening or updating a pull request runs these as separate lines in the checks
list, defined in `.github/workflows/ci.yml`:

| Check | Runs | Blocks on |
| --- | --- | --- |
| **Regression tests** | `go vet`, then `TestRegression_…` with the race detector | any behaviour that changed |
| **Smoke tests** | in-process smoke tests, then starts the real demo and runs `scripts/smoke.mjs` | anything that does not start or load |
| **Security tests** | `TestSecurity_…`, the page-escaping tests, `npm audit`, and a non-blocking `govulncheck` report | a failing security test or a high npm vulnerability |
| **Load tests** | `TestLoad_…`, the benchmark, then k6 against the real demo | errors, lost money, or a slow answer |
| **test** (existing) | frontend build and tests, backend tests against PostgreSQL | as before |

You can also run them by hand from the **Actions** tab (**Run workflow**).

## Show the tests catching a bug

The best demonstration on a pull request is a change that should fail. The
helper breaks the code in one of three ways and restores it:

```
git checkout -b demo/break-it
node scripts/demo-break.mjs threshold        # or: password, credit
git commit -am "Lower NCBA's block line"
git push -u origin demo/break-it
gh pr create --draft --fill
```

Watch the checks. **Regression tests**, **Smoke tests** and **Load tests** go red
for `threshold`; **Security tests** go red for `password` and `credit`. Open the
failing job to show the exact test and message. Then fix it:

```
node scripts/demo-break.mjs restore
git commit -am "Restore the block line"
git push
```

and the checks go green. Close the draft pull request afterwards.

| Break | Fails | Because |
| --- | --- | --- |
| `threshold` | 11 tests across regression, smoke and load | NCBA blocks payments it should let through |
| `password` | `TestSecurity_NoPasswordsInAnyReply` | `/api/ops/state` shows customer passwords |
| `credit` | `TestSecurity_AmountsCannotCreateOrDestroyMoney` | a credit of -100,000 makes a balance negative |

## Findings the new tests exposed

Fixed in this change:

1. **`/api/ops/state` returned every customer's password.** It needs no
   sign-in, and the account record it returns included the password field.
   The field is no longer serialised.
2. **Interbank credits and returns accepted zero and negative amounts.** A
   credit of -100,000 took an account to -100,000. Both routes now answer 400.
3. **`npm audit`: two high-severity findings** in `vite` (the dev server) and
   `source-map-js`. `npm audit fix` updated `package-lock.json`; the build and
   tests still pass.

Open, and what to do about them:

| Finding | Why it is open |
| --- | --- |
| Go standard library: `govulncheck` in CI (Go 1.22) reports 42 findings for `demo-banks` and 37 for `backend`. On Go 1.25.5 it is 29 and 26 | Fixed by moving to a current Go patch release. The pinned version is a project decision, so the CI step reports and does not block. |
| `backend` dependencies `github.com/jackc/pgx/v5` v5.5.5 (fixed in 5.9.2) and `golang.org/x/text` v0.14.0 (fixed in 0.41.0) | Newer versions may require a newer Go than `go 1.22` in `go.mod`. Needs the Go version decision above, then `go get` and the Postgres tests. |
| `DEMO-GUIDE.md` contains the shared Kifaru demo password in a public repository | A deliberate choice made earlier. Rotate it, or move it to the git-ignored `demo-credentials.txt`, if the hosted demo should not be open. |

Accepted for the synthetic demo, because every service listens only on
`127.0.0.1` (and a test keeps it that way):

- The `/ops`, `/admin/reset` and `/interbank/…` routes have no sign-in. Anyone
  on the machine can reset a bank, hold an account or credit one.
- `/api/config` and the director state publish the demo customer password.
- `GET /mail` allows any website to read the mailbox (`Access-Control-Allow-Origin: *`).
- Sign-in has no rate limit or lockout for repeated wrong passwords.
- Pages set no `Content-Security-Policy` or framing headers. The director
  embeds the banks' pages in frames on other ports, so a blanket
  `X-Frame-Options` would break it.

Performance observation: `/api/ops/state` returns every transfer, credit and
event so far. After about 1,300 payments in a k6 run it was 194 KB per poll,
and a page polls it every 1.5 seconds. Fine for a presentation, worth capping
if the demo ever runs for long.

## Not covered yet

[TestWork.md](TestWork.md) also plans two things that do not exist yet:

- **Integration with the real Kifaru API.** The With Kifaru director replay,
  alerts routed only to Equity, the Kifaru risk-check tab and retraction on
  reset. The connector is tested against a stub of the Kifaru API
  (`kifaruStub`), not the real one.
- **Browser end-to-end tests** with Playwright for the customer pages, the ops
  pages and the director. Smoke tests check that the pages and their files
  load, and the page-escaping tests cover the browser code, but nothing yet
  clicks through the interface.
