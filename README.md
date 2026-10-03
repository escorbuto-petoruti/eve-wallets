# eve-wallets

A local web app that charts the evolution of the EVE Online wallets you can read: the personal wallet of each character that signed in, plus the corporation wallet divisions that character can read.

It is meant to be installed locally: one process on your machine, one SQLite file per person, reachable only from loopback. You sign in on the page with EVE SSO; there is no other binary to install and no EVE application to register yourself.

ESI only returns the current balance and 30 days of wallet journal. To get a longer history the app snapshots balances periodically, and it backfills the last 30 days from the journal.

## Requirements

- Go (version in `go.mod`).
- An EVE Online account. The sign-in uses an EVE application (a PKCE public client, embedded id) whose callback is `http://localhost:8088/auth/callback`.
- For corporation wallets, the character needs the in-game role Accountant or Junior_Accountant. Without it the corporation wallet is skipped (reported as `missing corporation role`), not an error.

`eve-auth` is no longer needed or used. Earlier versions took tokens from that CLI and required `EVE_AUTH_BIN` and `EVE_CLIENT_ID`; both variables are ignored now.

## Run it

```bash
go build -o eve-wallets ./cmd/eve-wallets
./eve-wallets serve
```

Open <http://localhost:8088> and choose "Sign in with EVE SSO".

The redirect URI is fixed at `http://localhost:8088/auth/callback`, so the server has to listen on port 8088 (the default `--addr 127.0.0.1:8088`). With another port `serve` still starts but prints a warning, because SSO login will not work.

### Signing in

- Any EVE character can sign in; there is no allowlist.
- Login requests these scopes: `esi-wallet.read_character_wallet.v1`, `esi-wallet.read_corporation_wallets.v1` and `esi-corporations.read_divisions.v1`.
- Signing in also registers the character for collection. Right after the login `serve` starts a collection cycle (it does not wait for the next interval); the page shows a "collecting" state until the first balances arrive.
- A session lasts 7 days. It is an `HttpOnly`, `SameSite=Lax` cookie with no `Secure` flag (plain http on localhost); only a hash of the session id is stored. "Sign out" in the page header ends it at once (a POST with a same-origin check). When the session expires the page asks you to sign in again.
- Each person sees only the wallets their characters can read. A corporation wallet is shared by the characters of that corporation that proved access with their own token.

### Adding characters

A signed-in user can register more characters under the same account: use "Add character" in the page header and sign in with EVE SSO as the other character. Its personal wallet then appears next to the others, and the header lists all your characters. The login scopes are global: an added character is asked for the same scopes as any login.

- A character that already belongs to another user is not taken silently. A confirmation page asks whether to move it. Only that character moves; the other user keeps its remaining characters and is deleted only when it is left with none.
- After a move, the previous user's corporation wallet links are dropped. They come back with the next collection, so those wallets can look empty until that cycle completes (longer when ESI rate limits the run).
- Signing in with a character that is attached to another user signs you in as its owner.
- Pending confirmations live in memory for 10 minutes and are lost when `serve` restarts; add the character again if one expires.

## Usage

| Command | What it does |
|---------|--------------|
| `eve-wallets serve [--addr 127.0.0.1:8088] [--db PATH] [--every 30m] [--no-collect] [--no-backfill]` | Serves the page and the sign-in and, in the background, takes a snapshot and backfills the journal every cycle. |
| `eve-wallets collect [--db PATH]` | One snapshot of every wallet of every registered character, prints a summary. |
| `eve-wallets backfill [--db PATH]` | Stores journal balances of the last 30 days. Idempotent. `serve` runs it every cycle unless `--no-backfill` is given, so run it by hand only for a one-off backfill without `serve`. |
| `eve-wallets wallets [--db PATH]` | Lists the wallets (id, kind, owner, division, displayed name and its source). No network. |
| `eve-wallets label [--db PATH] <wallet-id> <name...>` | Sets the name shown for a wallet. `--clear <wallet-id>` removes it. |

`collect` and `backfill` use the characters that already signed in through the page. With none registered they print a hint to run `eve-wallets serve`, sign in, and exit with code 1. `wallets` and `label` only read or write the database.

`serve` flags:

- `--addr`: default `127.0.0.1:8088`. Only loopback (`127.0.0.1`, `::1`, `localhost`) is accepted; anything else exits with code 2.
- `--every`: pause between collection cycles, default `30m`, minimum `1m`.
- `--no-collect`: serve existing data without calling ESI (a login then does not trigger a collection).
- `--no-backfill`: each cycle only takes snapshots (like `collect`) and does not read the journals. The cycle skips the backfill by itself when its snapshot was rate limited.

Environment variable: `EVE_WALLETS_DB` is the database path (the `--db` flag wins).

Default database: `$XDG_DATA_HOME/eve-wallets/wallets.db`, else `~/.local/share/eve-wallets/wallets.db`. The directory is created with mode 0700 and the database files (including `-wal` and `-shm`) with 0600.

### Run as a systemd user service

Not run in the development environment; adjust the path to where you installed the binary.

```ini
# ~/.config/systemd/user/eve-wallets.service
[Unit]
Description=eve-wallets

[Service]
ExecStart=%h/.local/bin/eve-wallets serve
Restart=on-failure

[Install]
WantedBy=default.target
```

```bash
systemctl --user daemon-reload
systemctl --user enable --now eve-wallets
```

No `PATH` entry or environment file is needed.

## Upgrading to the sign-in version (schema v3)

The database schema is migrated automatically when the app opens it, and a binary older than the database refuses to open it. This version migrates a v2 file to v3 (users, tokens, per-user wallet links, sessions). After that an older binary cannot open the file, so stop the app and copy the database file before the first run of the new binary, so you can go back:

```bash
cp ~/.local/share/eve-wallets/wallets.db ~/.local/share/eve-wallets/wallets.db.bak-v2
```

Existing wallets are linked to a user on the first collection cycle of a character whose token can read them, so nobody sees old data until they sign in and that cycle runs.

## The page

The page shows a balance history chart with one line per selected wallet, an optional Total line, time ranges (24 h, 7 d, 30 d, All), a table of latest balances, and the result of the last collection. `/api/status` also reports `journal_points`, the journal balances the last backfill saw (0 when it did not run); the page does not display it.

## Wallet names

Corporation divisions are shown as `<corporation> · <name>` and the character wallet as the character name, in the picker, the latest balances table and the chart legend. The displayed name is, in order of precedence:

1. your label (source `custom`),
2. the division name reported by ESI (source `esi`),
3. the default: `Division N` for a corporation wallet, the character name for a character wallet (source `default`).

A label is never overwritten by a collection. Rename from the CLI; the page shows the change when you refresh it (the only write endpoint of the HTTP API is sign out):

```bash
./eve-wallets wallets                       # find the wallet id
./eve-wallets label 3 Mining fund           # quotes are optional
./eve-wallets label --clear 3               # back to the ESI name or Division N
```

A name is 1-64 characters without control characters. `--db` goes before the wallet id, and an unknown or invalid id exits with code 1.

Division names come from ESI during collection. The login already requests the scope `esi-corporations.read_divisions.v1`, and ESI needs the in-game role Director for it. Without the scope nothing is requested. With the scope but without the Director role, the collection reports `missing Director role` as a skipped item (not an error). In both cases `Division N` or your labels are shown. Your own labels win over ESI names, so run `eve-wallets label --clear <wallet-id>` to see the ESI name of a division you already renamed. ESI only returns the divisions whose name is not the default, and the in-game default division names were not verified, so the fallback is `Division N`.

## How history works, and its limits

- Snapshots exist only while `serve` (or a manual or cron `collect`) runs. While it is off there are gaps.
- Every `serve` cycle (default 30 min, the first one right at start) also stores the journal balances of the last 30 days, so per-transaction detail is kept while `serve` runs. ESI only holds 30 days of journal: if more than 30 days pass without a backfill (`serve` or `backfill`), the detail of the older days is lost for good, and only snapshots remain for that gap.
- Backfill covers only the last 30 days (the ESI journal window), and only journal entries that carry a balance.
- ESI caching: wallet balance 2 min for characters and 5 min for corporations; journal 1 h. Rate limits: 150 tokens per 15 min for character wallets and 300 for corporation wallets (read from the ESI OpenAPI spec). On a rate limit the run is reported as partial and retried later; the journal backfill is skipped in a cycle whose snapshot was rate limited. If you hit rate limits, use `--no-backfill`.
- Money is stored as integer ISK cents, never floats. ESI amounts have up to four decimals (for example `3123652530.8712`); they are parsed exactly from the response text and rounded to the nearest cent (half away from zero) when stored.

## Security notes

- The server binds only to loopback and refuses other addresses. It also rejects requests whose `Host` is not a loopback name (DNS rebinding defense). It is not hardened for the internet and has no HTTPS.
- Refresh tokens are stored in plaintext in the SQLite file (directory 0700, file 0600), so anyone who can read that file can use them. They are never logged or returned by the API. To revoke the access, remove the application from the third-party applications page of your EVE account; also delete the database if you stop using the app.
- A rotated refresh token is saved before it is used.
- The page works offline: Chart.js is vendored and hash-verified (see `internal/web/static/VENDORED.md`).

## Architecture

- `cmd/eve-wallets`: CLI (`serve`, `collect`, `backfill`, `wallets`, `label`) and wiring.
- `internal/store`: SQLite (pure Go, `modernc.org/sqlite`) schema, migrations, users, tokens, sessions, wallet names and series queries.
- `internal/esi`: ESI client (ETag cache, wallets, journals, division names).
- `internal/sso`: EVE SSO client (PKCE, JWT validation) with the embedded client id and scopes.
- `internal/auth`: token source backed by the refresh tokens in the store.
- `internal/collector`: snapshot and backfill logic, graceful skips, wallet-to-user links.
- `internal/scheduler`: background collection loop.
- `internal/web`: sign-in, sessions, user-scoped JSON API and embedded page.

## Development

```bash
go test ./...
go test -race ./...   # needs a C compiler
go vet ./...
gofmt -l .
```

## Status

The tests use fakes (ESI and SSO) or a temporary SQLite database; `go test`, `go test -race` and `go vet` pass. Before the sign-in work the app was run against the real EVE ESI on 2026-10-03 from WSL2, with one character and one corporation: `backfill` (8 wallets and 7,740 journal points; a second run left the same rows, with no duplicates), `collect`, and `serve`. That run found that ESI amounts have four decimals, which is why money is rounded to the nearest cent when stored.

Not verified:

- The real EVE SSO flow end to end: the redirect `http://localhost:8088/auth/callback` has to be registered for the embedded client id, and that has not been checked, nor has a real login been done.
- The add and move character flow (SSO round trip as a second character, the confirmation page, the characters in the header) in a real browser and against the real EVE SSO; it is covered by fakes and structural tests only.
- The page (sign-in screen, header, sign out, session expiry, collecting state) in a real browser; its JavaScript has only structural tests and a syntax check.
- The migration of the real v2 database to v3 (it is covered by a test on a generated v2 file).
- The `serve` cycle that backfills the journal every time (and `--no-backfill`) is covered by fakes only; it has not been run against the real ESI.
- The division names path was run against the real ESI only with a character that is not a Director. The case where a Director receives the names (stored with source `esi`, and cleared for divisions that return to the default name) is covered by fakes only.
- The systemd unit above.
