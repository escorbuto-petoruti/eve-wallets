# eve-wallets

A local web app that charts the evolution of every wallet of every authenticated EVE Online character: the personal wallet plus the corporation wallet divisions the character can read.

ESI only returns the current balance and 30 days of wallet journal. To get a longer history the app snapshots balances periodically, and it can backfill the last 30 days from the journal.

## Requirements

- Go (version in `go.mod`).
- The [`eve-auth`](https://github.com/escorbuto-petoruti/eve-auth) CLI, on `PATH` or pointed to with `EVE_AUTH_BIN`. From a local clone of eve-auth:
  ```bash
  go build -o ~/.local/bin/eve-auth ./cmd/eve-auth
  ```
- An EVE SSO application registered at <https://developers.eveonline.com/applications> with:
  - callback `http://localhost:8087/callback`
  - scopes `esi-wallet.read_character_wallet.v1` and `esi-wallet.read_corporation_wallets.v1`
- For corporation wallets, the character needs the in-game role Accountant or Junior_Accountant. Without it the corporation wallet is skipped (reported as `missing corporation role`), not an error.

## Setup

```bash
export EVE_CLIENT_ID=<your-client-id>

# once per character
eve-auth login --scopes "publicData esi-wallet.read_character_wallet.v1 esi-wallet.read_corporation_wallets.v1"

go build -o eve-wallets ./cmd/eve-wallets
./eve-wallets backfill    # optional: last 30 days from the journals
./eve-wallets serve       # http://127.0.0.1:8088
```

## Usage

| Command | What it does |
|---------|--------------|
| `eve-wallets serve [--addr 127.0.0.1:8088] [--db PATH] [--every 30m] [--no-collect]` | Serves the charts and collects in the background. |
| `eve-wallets collect [--db PATH]` | One snapshot of every wallet, prints a summary. |
| `eve-wallets backfill [--db PATH]` | Stores journal balances of the last 30 days. Idempotent. `serve` does not run it. |
| `eve-wallets wallets [--db PATH]` | Lists the wallets (id, kind, owner, division, displayed name and its source). No network, no `eve-auth`. |
| `eve-wallets label [--db PATH] <wallet-id> <name...>` | Sets the name shown for a wallet. `--clear <wallet-id>` removes it. |

`serve` flags:

- `--addr`: default `127.0.0.1:8088`. Only loopback (`127.0.0.1`, `::1`, `localhost`) is accepted; anything else exits with code 2.
- `--every`: collection interval, default `30m`, minimum `1m`.
- `--no-collect`: serve existing data without calling ESI.

Environment variables:

| Variable | Meaning |
|----------|---------|
| `EVE_AUTH_BIN` | `eve-auth` executable, default `eve-auth`. |
| `EVE_CLIENT_ID` | Inherited by the `eve-auth` subprocess. |
| `EVE_WALLETS_DB` | Database path (the `--db` flag wins). |

Default database: `$XDG_DATA_HOME/eve-wallets/wallets.db`, else `~/.local/share/eve-wallets/wallets.db`. The directory is created with mode 0700 and the database files (including `-wal` and `-shm`) with 0600.

The page shows a balance history chart with one line per selected wallet, an optional Total line, time ranges (24 h, 7 d, 30 d, All), a table of latest balances, and the result of the last collection. Without data it asks you to run `eve-wallets collect`.

## Wallet names

Corporation divisions are shown as `<corporation> · <name>` and the character wallet as the character name, in the picker, the latest balances table and the chart legend. The displayed name is, in order of precedence:

1. your label (source `custom`),
2. the division name reported by ESI (source `esi`),
3. the default: `Division N` for a corporation wallet, the character name for a character wallet (source `default`).

A label is never overwritten by a collection. Rename from the CLI; the page shows the change when you refresh it (the HTTP API is read-only, there is no write endpoint):

```bash
./eve-wallets wallets                       # find the wallet id
./eve-wallets label 3 Mining fund           # quotes are optional
./eve-wallets label --clear 3               # back to the ESI name or Division N
```

A name is 1-64 characters without control characters. `--db` goes before the wallet id, and an unknown or invalid id exits with code 1.

Optionally, `eve-wallets collect` can fetch the division names from ESI. It needs the scope `esi-corporations.read_divisions.v1` (add it to the `eve-auth login --scopes` list) and the in-game role Director. Without them the names are simply not fetched and `Division N` or your labels are shown. ESI only returns the divisions whose name is not the default, and the in-game default division names were not verified, so the fallback is `Division N`.

## How history works, and its limits

- Snapshots exist only while `serve` (or a manual or cron `collect`) runs. While it is off there are gaps.
- Backfill covers only the last 30 days (the ESI journal window), and only journal entries that carry a balance.
- ESI caching: wallet balance 2 min for characters and 5 min for corporations; journal 1 h. Rate limits: 150 tokens per 15 min for character wallets and 300 for corporation wallets (read from the ESI OpenAPI spec). On a rate limit the run is reported as partial and retried later.
- Money is stored as integer ISK cents, never floats. ESI amounts have up to four decimals (for example `3123652530.8712`); they are parsed exactly from the response text and rounded to the nearest cent (half away from zero) when stored.

## Security notes

- The HTTP API has no authentication, so the server binds only to loopback and refuses other addresses.
- Tokens are never stored by this app. They come from `eve-auth token` on demand and are never logged.
- The page works offline: Chart.js is vendored and hash-verified (see `internal/web/static/VENDORED.md`).
- The app depends on the tab-separated output format of `eve-auth list`.

## Architecture

- `cmd/eve-wallets`: CLI (`serve`, `collect`, `backfill`, `wallets`, `label`) and wiring.
- `internal/store`: SQLite (pure Go, `modernc.org/sqlite`) schema, migrations, wallet names and series queries.
- `internal/esi`: ESI client (ETag cache, wallets, journals, division names).
- `internal/auth`: token source that shells out to `eve-auth`.
- `internal/collector`: snapshot and backfill logic, graceful skips.
- `internal/scheduler`: background collection loop.
- `internal/web`: JSON API and embedded page.

## Development

```bash
CGO_ENABLED=0 go test ./...   # no C compiler needed
go vet ./...
gofmt -l .
```

## Status

All unit and integration tests use fakes or a temporary SQLite database. The app has not been run against the real ESI or a real `eve-auth` session yet, and the chart UI has not been checked in a browser by a person.
