# Independent bank demo: build and test plan

## Goal and trust boundary

Build three separately running classroom simulations: NCBA, KCB and Equity.
Each has its own process, in-memory customer ledger, mock fraud engine and
operations console. A local interbank switch moves synthetic credits. Each
bank's Kifaru connector independently authenticates as that institution and
sends only protected identifiers to the existing Kifaru API. Kifaru is not
allowed to read a bank's local customer password or raw account number.

All names, accounts, passcodes and transfers in this exercise are synthetic.
The customer pages must say so clearly, use only demo credentials, and never
look like a live production sign-in or accept real bank credentials.

## Suggested build sequence

1. **Contracts first:** define bank config, customer session, transfer, credit,
   fraud event, Kifaru publication and switch message types. Give each service
   its own address and state.
2. **Customer simulations:** each bank serves a distinct customer experience.
   Add a demo-only account chooser/passcode, session expiry/logout, balances,
   beneficiary entry and transfer receipt. Show every attempted transfer in
   that bank's operations view.
3. **Independent fraud engines:** NCBA scores new beneficiaries and shared
   devices, blocks the repeated-device attempt, then confirms the first
   customer's fraud. KCB uses its own lower report threshold and permits the
   different-device payment. Equity has its own incoming ledger and withdrawal
   restriction.
4. **Local interbank switch:** validate the destination bank/account, deliver
   credits to that bank and return funds to their origin. Do not call external
   payment rails.
5. **Optional Kifaru connectors:** off means send no report. On means authenticate
   using that bank's institution-bound Kifaru demo account, submit a report
   through the public API and display the returned status/alert ID. Use an HMAC
   of identifiers with a per-run salt; never put raw identifiers in reports.
6. **Equity response:** poll the bank's authorized Kifaru alert view, show the
   alert and basis, then let an operator place a hold or return eligible funds.
7. **Director/autopilot:** show every service, step and switch event. Advancing
   should invoke the same bank endpoints a manual action uses. Reset all local
   services and choose connector mode before every run.
8. **Rehearsal guide:** document ports, startup order, demo-only users, manual
   steps, autopilot steps, reset procedure and failure fallback.

## Manual test plan

### Setup

1. Start PostgreSQL and Kifaru API using the repository's normal development
   setup. Confirm `/health` reports `status: ok`.
2. Start all bank services and the switch/director. Open the director plus a
   customer page and operations page for each bank in separate tabs.
3. Choose **Without Kifaru** and reset. Confirm all three connector indicators
   are off and all ledgers are empty.

### Without Kifaru

1. Sign into Grace's synthetic NCBA account and transfer KES 85,000 to the
   synthetic Equity mule account. Confirm NCBA's own score allows it and the
   Equity ledger receives it.
2. Attempt Peter's NCBA transfer from the same simulated device. Confirm NCBA's
   own rule blocks it and its fraud desk marks Grace's prior payment confirmed.
3. Confirm Equity has an ordinary credit and no Kifaru alert. Confirm the
   simulated NCBA fraud-mail message remains unread/unactioned.
4. Sign into Faith's synthetic KCB account from a different simulated device
   and send KES 150,000. Confirm KCB's own rule permits it and Equity receives it.
5. Attempt cash-out from Equity. Confirm the simulation records KES 235,000
   paid out. Reset before the next mode.

### With Kifaru

1. Prepare the isolated synthetic reporter track record if the policy requires
   it. Select **With Kifaru** and reset; verify connectors are on.
2. Repeat the same NCBA transfers and customer confirmation. Verify NCBA
   submits a protected report through Kifaru and Equity receives the resulting
   single-source alert.
3. In Equity operations, acknowledge/review and place a local account hold.
   Verify the customer app and Equity ledger show the restriction.
4. Repeat the KCB transfer from the different device. Verify Kifaru matches
   the destination, upgrades the alert, and Equity freezes the incoming credit.
5. Attempt cash-out and return funds. Verify the local ledgers and Kifaru
   receiver outcomes agree. Reset and verify scenario records are cleaned up.

## Automated test plan

### Unit tests

- Fraud scores at, below and above each bank's block/report threshold.
- Device reuse across two customers in one bank; different devices across banks.
- New beneficiary timing, amount validation, held-account transfer and
  available-balance invariants.
- Switch routing, unknown destination, idempotency and partial-failure handling.
- HMAC output format, stable matching within one run, and non-matching salts
  across resets. Assert raw account/customer identifiers never enter reports
  or logs.
- Kifaru connector disabled/enabled behavior, auth failure and API timeout.
- Hold, cash-out restriction, partial return, full return and repeat action.

### Integration tests

Use isolated temporary state for all bank servers and a disposable PostgreSQL
database for Kifaru. Run both modes with deterministic clocks/IDs where
possible. Assert transfer/credit/return balances across sender, switch and
receiver. In connected mode assert that Kifaru stores only protected
identifiers, routes to the right receiving institution, and records Equity's
own decision. In disconnected mode assert the Kifaru API receives no reports.
Also test reset idempotency and that one bank cannot mutate another bank's
local state except through the switch contract.

### Browser end-to-end tests

Use Playwright against the actual local services, not mocked API responses.

- Test each synthetic customer sign-in, transfer, blocked attempt and logout.
- Test operator connector toggles and Equity alert/hold/return actions.
- Run autopilot for both modes and compare expected ledger balances and alert
  states at every step.
- Open the Kifaru institution workspace in a separate browser context and
  verify the actual alerts appear there.
- Check errors, keyboard operation, readable status labels and responsive
  layout. Keep a discreet `DEMO` marker on customer pages and a clear simulation
  notice on the director and analyst pages.
- Reset, replay twice, and confirm the second run is isolated from the first.

## Completion criteria

- All three bank services run independently and expose separate customer and
  operations pages.
- Manual and autopilot paths share the same transaction and fraud code.
- Both scenarios produce consistent bank ledgers; only the connected run
  reaches Kifaru.
- Browser, integration and unit suites pass from a clean reset.
- The demo guide names every URL, startup command, demo-only login and recovery
  action. No live bank credentials or payment services are involved.
