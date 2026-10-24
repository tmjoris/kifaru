# Demo bank test work

The independent demo banks (`demo-banks/`) have no automated tests yet. This
is the work needed to cover them, the Kifaru risk check they call, and the
director's scripted replay. The Kifaru API already has Go tests
(`backend/*_test.go`, including `risk_check_test.go`) and the frontend has
`src/domain.test.ts`.

All data stays synthetic. Tests that need PostgreSQL drop every table, so
point `TEST_DATABASE_URL` only at a throwaway database such as `kifaru_test`,
never at the demo database.

## Expected results

Use these as the assertions. They match a run verified on 2026-10-24 with the
risk check in place.

### Bank fraud scores

| Signal | Score |
| --- | --- |
| Base | 0.10 |
| New device | +0.25 |
| New beneficiary (added within the hour) | +0.20 |
| Amount of KES 50,000 or more | +0.05 |
| Device used on another customer's account in the hour | +0.35 |
| Beneficiary just paid by another customer | +0.25 |
| Repeat payment to a beneficiary added this session | +0.25 |
| Kifaru: device reported by another institution | +0.35 |
| Kifaru: beneficiary flagged, corroborated or single-source review | +0.40 |
| Kifaru: beneficiary flagged, notice or known-risk list | +0.25 |
| Kifaru: beneficiary reported, awaiting corroboration | +0.20 |

Scores are capped at 0.99. Block lines: NCBA 0.80, KCB 0.75, Equity 0.80.
Report lines: NCBA 0.70, KCB 0.45, Equity 0.70.

### Manual run, connectors on

| Step | Expected |
| --- | --- |
| Grace (NCBA) sends KES 85,000 to Equity 0114417 from a new browser | Completed at 0.60, not reported |
| Grace sends to the same account again | Blocked at 0.85; every session on that browser ends and its NCBA sign-ins are refused until reset |
| NCBA fraud desk | Grace confirms the first payment; NCBA reports it to Kifaru as victim-confirmed |
| Faith (KCB) signs in from the same browser | KCB ops feed shows the Kifaru device warning |
| Faith sends KES 150,000 to Equity 0114417 | Blocked at 0.99 with both Kifaru reasons; KCB reports the blocked attempt |
| Equity | Two corroborated alerts, one from NCBA and one from KCB |
| Kifaru Risk check tab as KCB, with the run key | Account and device flagged, corroborated; an unrelated account and device not flagged |

### Director replay

| Step | Without Kifaru | With Kifaru |
| --- | --- | --- |
| 1. Grace, KES 85,000 | Completed at 0.60 | Completed at 0.60, not reported |
| 2. Peter, same phone, KES 120,000 | Blocked at 0.99; Grace confirms; email to Equity's mailbox | Blocked at 0.99; Grace confirms; single-source review to Equity |
| 3. Equity | Nothing to act on | Places a hold on 0114417 |
| 4. Faith, new phone, KES 150,000 | Completed at 0.60 | Blocked at 0.99 by KCB's pre-payment check; KCB's report corroborates NCBA's |
| 5. Cash-out | KES 235,000 paid out | KES 85,000 declined |
| 6. Return | Nothing to return | KES 85,000 returned to NCBA |
| Final balances | Grace 327,500; Faith 380,000; Kiprono 0 | Grace 412,500; Faith 530,000; Kiprono 0 |

Both columns were run end to end on 2026-10-24. Without Kifaru, NCBA's email
reaches the shared mailbox and Equity gets no alert.

### Sessions, receipts and the switch

| Case | Expected |
| --- | --- |
| Successful payment | Receipt with reference, date, payee, bank, account and amount; the reference also shows in the activity list |
| Blocked payment | Notice gives the payment's reference |
| No sign-in, transfer or withdrawal for `SESSION_IDLE_MINUTES` (default 15) | Next request returns 401 with "You were signed out after N minutes without activity"; ops feed logs it; page refreshes do not extend the session |
| Session ended by a fraud block | 401 message asks the customer to contact customer care |
| Same credit sent twice through the switch | Credited once; second answer has `"duplicate": true`; switch log shows "repeat ignored" |
| Same credit sent twice straight to the bank | Credited once |
| Same return sent twice | Refunded once |
| Switch message without `ref` | 400 |
| Failed credit or return | Not recorded as settled, so a retry is delivered |

## 1. Go unit tests in `demo-banks/`

Add `*_test.go` files in package `main`. Drive the handlers with `httptest`;
stub Kifaru and the switch with `httptest.Server` instances and point
`KIFARU_API_URL`, `NETWORK_URL` and `<BANK>_URL` at them.

- **Scoring:** each row of the score table, scores just below, at and above
  each bank's block and report lines, and the 0.99 cap.
- **Repeat payment:** the second payment to a beneficiary added this session
  is blocked from a new device and allowed from a known device (0.60).
- **Lockout:** a block ends every session on that device, later sign-ins from
  it return 403, other devices still sign in, and a reset clears the lockout.
- **Fraud desk:** a block confirms the account holder's own earlier payment to
  that beneficiary, and other customers' payments in the hour.
- **Risk check client:** connector off sends no request; connector on sends
  only `sha256:` identifiers; a flagged answer adds the right amount; a Kifaru
  error or timeout logs a warning and scores without it (fail open).
- **Hashing:** `protect()` output format, the same value matching within one
  run key and not across run keys, and the browser's `protectIdentifier`
  agreeing with it (already covered on the frontend side).
- **Ledger:** amount validation, available-balance checks, held-account
  transfers and withdrawals refused, frozen-on-arrival credits, partial and full
  returns, and repeating a return.
- **Switch:** unknown destination, missing reference, credit failure refunding
  the sender, return routing, and every row of the sessions, receipts and
  switch table above. Send two identical messages at once and check the
  account is credited once; the second gets 409 or a duplicate answer,
  depending on timing.
- **Sessions:** expiry after the idle limit (set a short limit through
  `SESSION_IDLE_MINUTES` or a clock you control), activity extending it,
  `/api/me` leaving it unchanged, and the reason returned after expiry or a
  fraud block.
- **No raw identifiers:** reports and logs never contain a clear account
  number, customer name or device ID.

## 2. Integration tests

Run the real Kifaru API against a disposable database (`TEST_DATABASE_URL`)
and all three banks plus the network on free ports, each with fresh state.
Skip when the database variable is unset.

- Both director modes, asserting the director table above at every step:
  balances across sender, switch and receiver, Kifaru statuses and alert tiers.
- Connected mode: Kifaru stores only protected identifiers, routes alerts only
  to Equity, and records Equity's own hold and recovery decisions.
- Disconnected mode: the Kifaru API receives no reports and no risk checks.
- Reset twice and confirm the second run never matches the first (new run key,
  earlier reports retracted).
- One bank cannot change another bank's state except through the switch.

## 3. Browser end-to-end tests

Use Playwright (`@playwright/test`) against the real local services, never
mocked responses. A working script for the manual run, the Kifaru analyst
views and the director replay was used on 2026-10-24; turn it into specs under
`e2e/` with a config that starts the API, Vite and the banks.

- Each customer sign-in, transfer receipt, blocked attempt with its
  reference, forced sign-out, idle sign-out and sign-out.
- Operator connector toggles, Equity alert, hold and return.
- The director in both modes, checking ledgers and alert states at each step.
- Kifaru in a separate browser context: Equity's **Alerts received**, NCBA's
  **Signals submitted**, and KCB's **Risk check** tab, flagged and not flagged.
- Light and dark themes, phone width with no horizontal scroll, keyboard
  operation and readable status labels. The `DEMO` marker on customer pages and
  the simulation notice on the director and ops pages stay visible.
- Reset, replay twice, and confirm the runs are isolated.

The browser console currently shows a 404 for `/favicon.ico` on the bank and
director pages, and expected 401s from session checks. Serve a favicon or
filter these so the suite can fail on real console errors.

## Definition of done

- `go test ./...` passes in `demo-banks/` and `backend/`, with the integration
  suite run against a throwaway database.
- `npm test` and the Playwright suite pass from a clean reset.
- `DEMO-GUIDE.md` matches the tested behaviour.
