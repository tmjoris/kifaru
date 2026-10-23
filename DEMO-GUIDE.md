# Kifaru demo guide

Everything in the demo is synthetic. The Kifaru staff and analyst accounts in
the table use this shared password:

**`KifaruDemo2026!`**

## Logins

| Who | Where | Email | Institution to pick |
| --- | --- | --- | --- |
| Kifaru staff (runs the replay) | `/staff` | `anthonyjordan@kifaru.co.ke` | none |
| Equity Bank (receives alerts) | `/` | `anthonyjordan@equitybank.co.ke` | Equity Bank |
| NCBA (first sending bank) | `/` | `anthonyjordan@ncba.co.ke` | NCBA |
| KCB (second sending bank) | `/` | `anthonyjordan@kcb.co.ke` | KCB |

Every other bank's Kifaru analyst logins are in `demo-credentials.txt`.

## Sign in to Kifaru

Use the Kifaru frontend URL for both staff and institution analysts. Locally,
that is `http://127.0.0.1:5173`.

- **Kifaru staff:** open `http://127.0.0.1:5173/staff`. Enter
  `anthonyjordan@kifaru.co.ke` and `KifaruDemo2026!`, then select **Sign in**.
  The staff page has no institution selector.
- **Bank analyst in Kifaru:** open `http://127.0.0.1:5173/`. Enter the
  institution email from the table and `KifaruDemo2026!`. Select the matching
  institution from the **Institution** menu, then select **Sign in**. For
  example, use `anthonyjordan@ncba.co.ke` with **NCBA** selected.

For the hosted demo, replace `http://127.0.0.1:5173` with the deployed frontend
URL. The password for these Kifaru accounts is `KifaruDemo2026!`.

## Start everything (local)

Run each block in its own terminal, from the repository root. The first time,
run `npm ci` to install the frontend dependencies.

Bash (macOS, Linux, Git Bash):

```bash
# 1. Postgres (skip if you already have one)
docker run -d --rm --name kifaru-pg -e POSTGRES_PASSWORD=kifaru -e POSTGRES_DB=kifaru -p 55432:5432 -v kifaru-pgdata:/var/lib/postgresql/data postgres:16

# 2. API
cd backend && DATABASE_URL="postgres://postgres:kifaru@localhost:55432/kifaru?sslmode=disable" PORT=8000 go run .

# 3. Frontend
npm run dev
```

PowerShell (Windows):

```powershell
# 1. Postgres (skip if you already have one)
docker run -d --rm --name kifaru-pg -e POSTGRES_PASSWORD=kifaru -e POSTGRES_DB=kifaru -p 55432:5432 -v kifaru-pgdata:/var/lib/postgresql/data postgres:16

# 2. API
cd backend; $env:DATABASE_URL="postgres://postgres:kifaru@localhost:55432/kifaru?sslmode=disable"; $env:PORT="8000"; go run .

# 3. Frontend
npm run dev
```

The `kifaru-pgdata` volume keeps the database when the container stops. The API
re-creates the demo logins on every start, so the passwords above always work.
To start from an empty database, stop the container and run
`docker volume rm kifaru-pgdata`.

PowerShell `$env:` variables last only for that terminal window, so set them
again in a new one. If `docker` reports that it cannot connect to
`dockerDesktopLinuxEngine`, start Docker Desktop. If it then keeps returning
`500 Internal Server Error`, quit Docker Desktop, run `wsl --shutdown`, and
start it again.

Then open <http://127.0.0.1:5173/staff>. To use the hosted version, open your
Render frontend URL and add `/staff`. Leave time for the deploy first: the
replay needs migration `009`.

## Independent bank apps

The separate-service rehearsal lives in `demo-banks/`. Each service binds to
localhost and uses synthetic classroom accounts only. Start the Kifaru API as
above, then run the demo services in a separate terminal:

```bash
cd demo-banks && go run .
```

The pages are embedded in the program, so restart `go run .` after any change
to files in `demo-banks/web`. If the default ports are taken, set
`NETWORK_PORT`, `NCBA_PORT`, `KCB_PORT` and `EQUITY_PORT` to free ports, and
use those in the URLs below. For example:

```bash
cd demo-banks && NCBA_PORT=9201 go run .
```

```powershell
cd demo-banks; $env:NCBA_PORT="9201"; go run .
```

The three services have independent customer profiles, balances, fraud rules,
analyst feeds, and transfer ledgers. Open these URLs in separate tabs:

| Bank | Customer banking | Bank analyst workspace |
| --- | --- | --- |
| NCBA | <http://127.0.0.1:9101> | <http://127.0.0.1:9101/ops> |
| KCB | <http://127.0.0.1:9102> | <http://127.0.0.1:9102/ops> |
| Equity | <http://127.0.0.1:9103> | <http://127.0.0.1:9103/ops> |

Each customer portal has its own look: NCBA is espresso and gold with a serif
heading and a left sidebar, KCB is green with a top navigation bar, and Equity
is charcoal and red with a dark sidebar. The sign-in page shows a branded
illustration beside the login card. After sign-in, each portal shows:

- a balance card with a show/hide toggle for the amount
- money-out, money-in and blocked-payment totals
- recent transactions and incoming payments
- a **Send money** form for cross-bank payments
- **Overview**, **Transfers** and **My account** views

**Continue** on the payment form opens a review sheet. Check the details, then
select **Confirm and send**. The result appears as a toast in the corner: green
when the transfer is sent, red when the bank's own fraud checks block it or the
amount is more than the available balance. The page pauses its automatic
refresh while the sheet is open, and your form entries survive the refresh.

A small `DEMO` marker beside the bank name, the sign-in footer and the page
footer identify the synthetic portals. Sign in with the selected customer
profile and password `bank1234`; this is separate from the Kifaru analyst
password. Never enter real banking credentials.

The `/ops` analyst pages use a dark console. They need no sign-in. Each shows:

- a row of counters: transfers scored, blocked, Kifaru alerts and credits in
- the bank's fraud engine, with a score gauge and a marker at the block
  threshold for every transfer
- incoming Kifaru alerts, coloured by tier, with **Place account hold** and
  **Return available funds**
- incoming interbank credits, the local operations feed and the mailbox

The **Kifaru connector** switch in the page header turns report submission on
or off for that bank.

Open <http://127.0.0.1:9100> for the director. Choose **Without Kifaru** or
**With Kifaru**, then select **Next attacker step** to advance one real
transaction per click. The progress panel lists the six scenario steps and
shows how many are done. Below it are the attacker activity console, the
interbank switch log, the shared mailbox and the three banks' `/ops` pages in
frames, each with links to open that bank's customer and operations pages in
their own tab. The frames load once and are not reloaded while you present.

For a manual run, sign in to a customer tab and make a transfer. Each bank
blocks a transfer whose score reaches its block threshold. On NCBA, a first
payment from a new device to a new beneficiary scores 0.60 and goes through. A
second payment from the same customer to that beneficiary adds 0.25 and is
blocked, and so is a payment from a device another customer just used. A block
signs out every session on that device and refuses its sign-ins until the next
reset. The fraud desk then calls the customers, account holder included, who
recently paid the same beneficiary, and they confirm the fraud. Reset from the
director to sign in from that browser again. When that
bank's Kifaru connector is enabled,
flagged transfer reports use Kifaru's report API and appear in the staff SOC
event stream with the sending bank and policy outcome. The receiving bank's
analyst tab shows the resulting alert when policy issues one.

For a live connected run, select **Prepare reporter history** in the director.
It ensures NCBA has enough synthetic, upheld alerts through Kifaru's API to
meet the single-source reporter-standing rule. It is safe to run again and
does not keep adding history once NCBA qualifies. Use only the demo database.
See [PLAN.md](PLAN.md) for the manual exercise and automated test plan. The
staff-portal replay remains available as a fallback.

## Screen setup

- **Kifaru staff:** open `http://127.0.0.1:5173/staff` and sign in with the
  staff email and password above. The SOC stream shows flagged transfers sent
  by each bank connector.
- **Customer tabs:** open each bank's customer URL above and sign in as Grace,
  Faith, and J. Kiprono (the first profile is selected by default). Each origin
  has its own browser session.
- **Analyst tabs:** open each bank's `/ops` URL above. Turn the **Kifaru
  connector** switch on in each bank where you want reports sent. During the
  attack, Equity's analyst workspace shows incoming alerts and holds. The
  director also embeds these three pages, so a separate tab is only needed to
  show one bank full screen.
- **Attack director:** keep `http://127.0.0.1:9100` available to step through
  the comparison replay.
- **Fit:** if the replay doesn't fit the projector, zoom the browser to 90%.

## Running the fake attack

**→, Page Down or Space** advances one step, so a presentation clicker works.

### Run 1: without Kifaru

1. Click **Without Kifaru** at the top right. This also resets the replay.
2. Press → five times and talk through each step:
   1. **NCBA takeover:** the attacker phished Grace's login and sends KES 85,000 to a three-day-old Equity account. NCBA's engine scores it 0.58, below its 0.80 block line, so it goes through.
   2. **NCBA blocks the second attempt:** the same phone tries Peter's account. NCBA blocks it and Grace confirms the fraud. NCBA can only email Equity's fraud mailbox, and nobody reads it.
   3. **Equity:** the account looks normal and the money is free to withdraw.
   4. **KCB:** the attacker uses a new phone on Faith's KCB account and sends KES 150,000. KCB has never seen this account and lets it through.
   5. **Cash-out:** KES 235,000 is gone. Equity reads the email the next morning.

### Run 2: with Kifaru

1. Click **Replay with Kifaru** (or **With Kifaru** at the top).
2. Press → five times:
   1. **NCBA takeover:** same as before. NCBA has no reason to report yet.
   2. **NCBA blocks the second attempt:** NCBA publishes a protected signal. Kifaru sends Equity a **single-source review** at once, because the victim confirmed the fraud and NCBA has a strong record. *Switch to window 2: the alert is there.*
   3. **Equity:** the fraud desk places a hold. Money can arrive but can't leave.
   4. **KCB:** KCB's weaker flag matches NCBA's signal even though the phone changed. The alert escalates to **corroborated**, and the KES 150,000 is frozen when it arrives.
   5. **Cash-out:** the withdrawal is declined. Both transfers go back, and **KES 0** is lost.
3. The comparison panel at the bottom gives you the closing line.

## Reset and recovery

- **Reset:** the **Reset** button clears the run and deletes everything the replay wrote to Kifaru. Reset before each rehearsal and once more just before you present.
- **If something errors:** click **Reset**, choose the mode again, and step through.
- **About NCBA's record:** the "nine upheld alerts" exists only inside the replay. It never touches NCBA's real standing, so you can say so if you're asked.
