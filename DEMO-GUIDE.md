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
Leave time for a new deploy to finish first: the replay needs migration `009`.

## Start from scratch

This takes you from a computer with nothing installed to a finished run. It
works on Windows 10 or 11 (PowerShell) and on Ubuntu 22.04 or 24.04. Everything
runs on your own machine and uses synthetic data only. Allow about 30 minutes,
most of it downloads.

### 1. Install the tools

You need four tools:

| Tool | Version | Used for |
| --- | --- | --- |
| Git | any | getting the code |
| Go | 1.22 or later | the Kifaru API and the three demo banks |
| Node.js | 22.18 or later, or 24 | the Kifaru web app |
| Docker | any recent | the PostgreSQL database |

**Windows (PowerShell).** `winget` comes with Windows 11 and current Windows 10.

```powershell
winget install --id Git.Git -e
winget install --id GoLang.Go -e
winget install --id OpenJS.NodeJS.LTS -e
winget install --id Docker.DockerDesktop -e
```

Close PowerShell and open a new window so the new tools are on your path. Then
start **Docker Desktop** and wait until it says it is running. The first start
may ask you to turn on WSL 2 and restart the PC.

**Ubuntu.**

```bash
sudo apt update
sudo apt install -y git curl docker.io
sudo snap install go --classic
sudo snap install node --classic --channel=22
sudo usermod -aG docker "$USER"
```

Log out and back in so the `docker` group applies. (Until then, put `sudo`
before each `docker` command.) Ubuntu's own `golang` package is too old, which
is why Go comes from `snap`.

**Check the versions** (same on both systems):

```
git --version
go version
node --version
docker --version
```

Go must be 1.22 or later and Node v22.18 or later. If either is older, install
a newer one before going on.

### 2. Get the code

```
git clone https://github.com/tmjoris/kifaru.git
cd kifaru
npm ci
```

`main` has everything you need. `npm ci` installs the web app's dependencies and
only has to run once.

`demo-credentials.txt` is not in the repository. You do not need it for this
guide, because the logins you use are in the table above. It only holds the
analyst logins for the other licensed banks. If you want it, copy it from the
machine that has it into the repository root.

### 3. Start the four services

Open four terminals in the `kifaru` folder and start them in this order. Each
one keeps running, so leave it open.

**Terminal 1: database.**

```
docker run -d --rm --name kifaru-pg -e POSTGRES_PASSWORD=kifaru -e POSTGRES_DB=kifaru -p 55432:5432 -v kifaru-pgdata:/var/lib/postgresql/data postgres:16
```

The first run downloads the image. Wait about five seconds after it returns
before starting the API. The `kifaru-pgdata` volume keeps your data when the
container stops.

**Terminal 2: Kifaru API.**

Ubuntu:

```bash
cd backend
DATABASE_URL="postgres://postgres:kifaru@localhost:55432/kifaru?sslmode=disable" PORT=8000 go run .
```

Windows:

```powershell
cd backend
$env:DATABASE_URL="postgres://postgres:kifaru@localhost:55432/kifaru?sslmode=disable"
$env:PORT="8000"
go run .
```

It is ready when it prints `KIFARU Go API listening on :8000`. On the first
start it creates the tables and the demo logins. If it stops with
`connect to PostgreSQL`, the database was not ready yet. Wait a few seconds and
run it again. It re-creates the demo logins on every start, so the passwords in
this guide always work.

**Terminal 3: Kifaru web app.**

```
npm run dev
```

It is ready when it shows a local address of `http://127.0.0.1:5173/`.

**Terminal 4: the three demo banks and the director.** Start the API first,
because the banks talk to it.

```
cd demo-banks
go run .
```

It is ready when it prints that the director is listening on port 9100 and
each bank on ports 9101, 9102 and 9103.

### 4. Check that it works

| Service | Address | What you should see |
| --- | --- | --- |
| Kifaru API | <http://127.0.0.1:8000/health> | a short JSON reply |
| Kifaru web app | <http://127.0.0.1:5173/staff> | the staff sign-in page |
| Director | <http://127.0.0.1:9100> | the scenario page with three bank frames |
| NCBA, KCB, Equity | ports 9101, 9102, 9103 | each bank's sign-in page |

Sign in to the staff page with the staff email and password from the **Logins**
table. If you get in, the API and the database are working.

If something is wrong:

- **A port is taken.** Stop whatever uses it, or move the demo banks with the
  variables in the table below. The API port is the `PORT` variable.
- **`docker` cannot connect to `dockerDesktopLinuxEngine` (Windows).** Start
  Docker Desktop. If it then keeps returning `500 Internal Server Error`, quit
  Docker Desktop, run `wsl --shutdown`, and start it again.
- **`docker: permission denied` (Ubuntu).** Log out and back in after the
  `usermod` step, or put `sudo` before the command.
- **A PowerShell variable seems to be ignored.** `$env:` values last only for
  that terminal window. Set them again in a new one.
- **The director says to start all three banks.** Terminal 4 is not running, or
  it stopped. Start it again.
- **You changed a file in `demo-banks/web`.** The pages are built into the
  program, so stop terminal 4 with Ctrl+C and run `go run .` again.

### 5. Settings you can change

The defaults suit a single machine. Set a variable in the terminal before `go run .`
(`$env:NAME="value"` on Windows, `NAME=value` before the command on Ubuntu).

| Variable | Used by | Default | What it does |
| --- | --- | --- | --- |
| `DATABASE_URL` | API | none, required | PostgreSQL connection string |
| `PORT` | API | `8000` | API port |
| `NETWORK_PORT` | demo banks | `9100` | director and interbank switch port |
| `NCBA_PORT`, `KCB_PORT`, `EQUITY_PORT` | demo banks | `9101`, `9102`, `9103` | each bank's port |
| `KIFARU_API_URL` | demo banks | `http://127.0.0.1:8000` | where the banks send reports and risk checks. Set it to the hosted API to run against the hosted demo. |
| `KIFARU_WEB_URL` | demo banks | `http://127.0.0.1:5173` | where the director's Kifaru staff link points |
| `KIFARU_DEMO_PASSWORD` | demo banks | `KifaruDemo2026!` | the Kifaru analyst password the banks sign in with. Keep it the same as the password at the top of this guide. |
| `NETWORK_URL` | demo banks | the director's own address | where the banks find the interbank switch |
| `DEMO_HASH_KEY` | demo banks | built-in demo key | key used to hash account and device details before they are sent |
| `SESSION_IDLE_MINUTES` | demo banks | `15` | idle time before a customer is signed out |

### 6. Run the demo from start to finish

1. Complete steps 1 to 4 and sign in to the staff page once.
2. Open the director at <http://127.0.0.1:9100> and, in other tabs, the three
   customer pages listed under **Independent bank apps**.
3. In the director, select **Prepare reporter history**. It is safe to repeat.
4. **Run 1.** Select **Without Kifaru**. This resets all three banks and
   switches their Kifaru connectors off. Select **Next attacker step** six
   times and talk through each step.
5. **Run 2.** Select **With Kifaru**. This resets everything again and switches
   all three connectors on, so you do not need to use the connector switches.
   Select **Next attacker step** six times. Watch the Equity analyst frame for
   the alert and the staff page for the SOC events.
6. To rehearse again, select **Without Kifaru** or **With Kifaru** once more.
   Reset once more just before you present.

For a manual run instead of the director, sign in to a customer tab, turn the
**Kifaru connector** on in that bank's `/ops` page, and make transfers by hand.

### 7. Stop everything

Press Ctrl+C in terminals 4, 3 and 2. Then stop the database:

```
docker stop kifaru-pg
```

The container removes itself when it stops, and your data stays in the
`kifaru-pgdata` volume. To wipe the database and start clean, run
`docker volume rm kifaru-pgdata` after stopping the container.

## Independent bank apps

The separate-service rehearsal lives in `demo-banks/`. Each service binds to
localhost and uses synthetic classroom accounts only. This is terminal 4 in
**Start from scratch**. Start the Kifaru API first, then run the demo services in
a separate terminal:

```
cd demo-banks
go run .
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
With Kifaru, step 4 ends at KCB: its pre-payment check finds NCBA's flag on
Equity 0114417 and blocks Faith's KES 150,000 before it leaves, so only NCBA's
KES 85,000 reaches the mule account, and Equity returns it.

For a manual run, sign in to a customer tab and make a transfer. Each bank
blocks a transfer whose score reaches its block threshold. On NCBA, a first
payment from a new device to a new beneficiary scores 0.60 and goes through. A
second payment from the same customer to that beneficiary adds 0.25 and is
blocked, and so is a payment from a device another customer just used. A block
signs out every session on that device and refuses its sign-ins until the next
reset. The fraud desk then calls the customers, account holder included, who
recently paid the same beneficiary, and they confirm the fraud. Select
**Without Kifaru** or **With Kifaru** in the director to reset and sign in from
that browser again. When that bank's Kifaru connector is enabled, flagged
transfer reports use Kifaru's report API and appear in the staff SOC
event stream with the sending bank and policy outcome. The receiving bank's
analyst tab shows the resulting alert when policy issues one.

With the connector on, each bank also asks Kifaru's risk check when a customer
signs in and before it scores a transfer. A beneficiary account that another
bank reported adds 0.20 to 0.40 to the score, depending on the tier, and a
device that another bank reported adds 0.35. The block decision stays with
each bank. All three banks read the same device ID cookie, so after NCBA
blocks Grace's repeat payment, signing in to Faith's KCB account from that
browser is flagged, and KCB blocks a payment to Equity 0114417 before it
leaves. Each bank's `/ops` page lists the Kifaru reasons and shows the demo run
key.

In Kifaru, the **Risk check** tab of each institution workspace runs the same
check by hand. Enter a beneficiary account and its institution, a device ID,
or both, and the demo run key from any bank's `/ops` page. The browser hashes
them before sending. Kifaru answers only flagged or not flagged, the strongest
tier and how many institutions reported it.

For a live connected run, select **Prepare reporter history** in the director.
It ensures NCBA has enough synthetic, upheld alerts through Kifaru's API to
meet the single-source reporter-standing rule. It is safe to run again and
does not keep adding history once NCBA qualifies. Use only the demo database.
See [TestWork.md](TestWork.md) for the expected results and the test plan.
The staff-portal replay remains available as a fallback.

Each successful payment opens a receipt with its reference, and a blocked
payment's notice gives its reference too. A customer who signs in, sends or
withdraws nothing for 15 minutes is signed out with a message saying why.
Set `SESSION_IDLE_MINUTES` before `go run .` to change the limit. The switch
settles each transfer reference once, so a repeated credit or return changes
nothing.

## Screen setup

- **Kifaru staff:** open `http://127.0.0.1:5173/staff` and sign in with the
  staff email and password above. The SOC stream shows flagged transfers sent
  by each bank connector.
- **Customer tabs:** open each bank's customer URL above and sign in as Grace,
  Faith, and J. Kiprono (the first profile is selected by default). Each origin
  has its own browser session, but all three share one device ID.
- **Analyst tabs:** open each bank's `/ops` URL above. Turn the **Kifaru
  connector** switch on in each bank where you want reports sent. During the
  attack, Equity's analyst workspace shows incoming alerts and holds. The
  director also embeds these three pages, so a separate tab is only needed to
  show one bank full screen.
- **Attack director:** keep `http://127.0.0.1:9100` available to step through
  the comparison replay.
- **Fit:** if the replay doesn't fit the projector, zoom the browser to 90%.

## Running the fake attack

This section describes the replay in the Kifaru staff portal. The bank director
at port 9100 runs the same story with real transfers across the three bank
apps; it uses **Next attacker step** and is covered in **Run the demo from start
to finish** above.

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
