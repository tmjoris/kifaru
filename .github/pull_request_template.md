## What changed

## How it was checked

The checks below run on every pull request. A red one links to the exact test that failed.

- **Regression tests**: scores, ledger, switch, sessions, Kifaru connector and the director replay still give the agreed answers.
- **Smoke tests**: the pages load and the main journeys work, in-process and against the running demo.
- **Security tests**: authentication, isolation, hostile input, protected identifiers, and known vulnerable packages.
- **Load tests**: many customers at once stay correct and fast, and no money is created or lost.
- **test**: the Kifaru API against PostgreSQL, and the web app.

See [TESTING.md](../TESTING.md) for what each one covers and how to run it locally.
