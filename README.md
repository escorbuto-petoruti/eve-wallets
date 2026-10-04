# eve-wallets

A local web app that charts the evolution of the EVE Online wallets you can read: the personal wallet of each character that signed in, plus the corporation wallet divisions that character can read.

> eve-wallets is not affiliated with or endorsed by CCP Games. EVE Online and the related marks and logos are trademarks of CCP hf.

It is meant to be installed locally: one process on your machine, one SQLite file per person, reachable only from loopback. You sign in on the page with EVE SSO; there is no other binary to install and no EVE application to register yourself.

ESI only returns the current balance and 30 days of wallet journal. To get a longer history the app snapshots balances periodically, and it backfills the last 30 days from the journal.

## Requirements

- Go (version in `go.mod`) only if you build from source; the release archives are self-contained.
- An EVE Online account. The sign-in uses a shared EVE application (a PKCE public client, embedded id) whose only redirect is `http://localhost:8088/auth/callback` (see [Sign-in and port 8088](#sign-in-and-port-8088)).
- For corporation wallets, the character needs the in-game role Accountant or Junior_Accountant. Without it the corporation wallet is skipped (reported as `missing corporation role`), not an error.

`eve-auth` is no longer needed or used. Earlier versions took tokens from that CLI and required `EVE_AUTH_BIN` and `EVE_CLIENT_ID`; both variables are ignored now.

## Install

Binaries are published as GitHub releases for linux, darwin (macOS) and windows. Each release has one archive per platform and a `checksums.txt`:

| Platform | Archive |
|----------|---------|
| Linux | `eve-wallets_<version>_linux_amd64.tar.gz`, `eve-wallets_<version>_linux_arm64.tar.gz` |
| macOS | `eve-wallets_<version>_darwin_amd64.tar.gz`, `eve-wallets_<version>_darwin_arm64.tar.gz` |
| Windows | `eve-wallets_<version>_windows_amd64.zip` |

`<version>` is the tag without the leading `v`. Each archive holds the binary (`eve-wallets`, `eve-wallets.exe` on Windows), `LICENSE` and `README.md`.

### Installer script (Linux and macOS)

```bash
curl -fsSL https://raw.githubusercontent.com/escorbuto-petoruti/eve-wallets/main/install.sh | sh
```

It picks your platform, downloads the latest release, verifies its SHA-256 against `checksums.txt` and installs the binary to `~/.local/bin` (never with sudo). Options: `VERSION=vX.Y.Z` installs a specific release, `INSTALL_DIR=DIR` another directory, and `--systemd` also installs the user service described [below](#run-as-a-systemd-user-service). An existing binary is kept as `eve-wallets.bak-prev`.

If you prefer to read it before running it:

```bash
curl -fsSL -o install.sh https://raw.githubusercontent.com/escorbuto-petoruti/eve-wallets/main/install.sh
less install.sh
sh install.sh
```

### Installer script (Windows)

In PowerShell (5.1 or 7), no administrator rights needed:

```powershell
irm https://raw.githubusercontent.com/escorbuto-petoruti/eve-wallets/main/install.ps1 | iex
```

It downloads the latest release zip, verifies its SHA-256 against `checksums.txt` before installing anything, and puts `eve-wallets.exe` in `%LOCALAPPDATA%\eve-wallets\bin`. Options are environment variables: `$env:VERSION = 'vX.Y.Z'` installs a specific release and `$env:INSTALL_DIR = 'DIR'` another directory. An existing binary is kept as `eve-wallets.exe.bak-prev`. It needs no administrator rights. If the directory is not on your `PATH`, in an interactive terminal it asks `Add <dir> to your user PATH? [y/N]` (default No); say yes and a new terminal finds `eve-wallets`. Only the user PATH is touched, never the machine PATH, and running it again does not duplicate the entry. `$env:EVE_WALLETS_ADD_TO_PATH = '1'` adds it without asking and `'0'` never asks nor adds; with it unset and no interactive terminal (for example automation through `irm | iex`) nothing changes and the installer prints how to add it. Only Windows amd64 is supported. If `eve-wallets.exe` is running, stop it first (the installer cannot replace a running program and says so).

If you prefer to read it before running it:

```powershell
irm https://raw.githubusercontent.com/escorbuto-petoruti/eve-wallets/main/install.ps1 -OutFile install.ps1
notepad install.ps1
.\install.ps1
```

To update, run the same installer again. If scripts are blocked on your machine, use `powershell -ExecutionPolicy Bypass -File .\install.ps1`.

### Manual download (all platforms)

Download the archive for your platform and `checksums.txt` from the releases page, then verify and extract it:

```bash
sha256sum --ignore-missing -c checksums.txt   # macOS: shasum -a 256 -c checksums.txt
tar -xzf eve-wallets_<version>_linux_amd64.tar.gz
```

The manual steps below are an alternative to the installers. On Windows, check the hash with `Get-FileHash <archive> -Algorithm SHA256`, compare it with the line in `checksums.txt`, and unzip. Put the binary somewhere on your `PATH`.

### From source

```bash
go install github.com/escorbuto-petoruti/eve-wallets/cmd/eve-wallets@latest
# or, from a checkout:
go build -o eve-wallets ./cmd/eve-wallets
```

A source build reports the version `dev`.

## Run it

```bash
eve-wallets serve
```

Open <http://localhost:8088> and choose "Sign in with EVE SSO". `eve-wallets version` prints the installed version.

### Sign-in and port 8088

The app uses a shared EVE application whose only registered redirect is `http://localhost:8088/auth/callback`, so the server has to listen on port 8088 (the default `--addr 127.0.0.1:8088`). With another port `serve` still starts but prints a warning, because SSO login will not work.

To use your own application instead, register one at <https://developers.eveonline.com> with the callback `http://localhost:8088/auth/callback` and the scopes listed under [Signing in](#signing-in), and start the app with its client id in `EVE_WALLETS_CLIENT_ID`. The callback stays fixed, so port 8088 is still required. The variable has to be set for every command that refreshes tokens (`serve`, `collect`, `backfill`), and characters that signed in with the shared application have to sign in again.

### Signing in

- Any EVE character can sign in; there is no allowlist.
- Login requests these scopes: `esi-wallet.read_character_wallet.v1`, `esi-wallet.read_corporation_wallets.v1`, `esi-corporations.read_divisions.v1` and `esi-characters.read_loyalty.v1` (loyalty points; see [Loyalty points](#loyalty-points)).
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
| `eve-wallets version` | Prints `eve-wallets <version>` (also `--version`). |
| `eve-wallets update [--check] [--force]` | Updates the binary to the latest release (Linux and macOS), see [Update](#update). |

`collect` and `backfill` use the characters that already signed in through the page. With none registered they print a hint to run `eve-wallets serve`, sign in, and exit with code 1. `wallets` and `label` only read or write the database.

`serve` flags:

- `--addr`: default `127.0.0.1:8088`. Only loopback (`127.0.0.1`, `::1`, `localhost`) is accepted; anything else exits with code 2.
- `--every`: pause between collection cycles, default `30m`, minimum `1m`.
- `--no-collect`: serve existing data without calling ESI (a login then does not trigger a collection).
- `--no-backfill`: each cycle only takes snapshots (like `collect`) and does not read the journals. The cycle skips the backfill by itself when its snapshot was rate limited.

Environment variables: `EVE_WALLETS_DB` is the database path (the `--db` flag wins); `EVE_WALLETS_CLIENT_ID` replaces the embedded EVE client id (see [Sign-in and port 8088](#sign-in-and-port-8088)); `EVE_WALLETS_UPDATE_API` overrides the GitHub API base URL used by `update` (for tests).

Default database: `$XDG_DATA_HOME/eve-wallets/wallets.db`, else `~/.local/share/eve-wallets/wallets.db`. On Windows, `%LOCALAPPDATA%\eve-wallets\wallets.db` (`%USERPROFILE%\AppData\Local` is used if `LOCALAPPDATA` is empty). The directory is created with mode 0700 and the database files (including `-wal` and `-shm`) with 0600.

### Run as a systemd user service

On Linux, a user service keeps `serve` running in the background and restarts it if it fails. Adjust the path if you installed the binary somewhere else (`%h` is your home directory).

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

No `PATH` entry or environment file is needed. After an update, restart it with `systemctl --user restart eve-wallets`.

On macOS you can run the same command from a launchd agent, and on Windows you can run `eve-wallets.exe serve` in a terminal (no service is provided). Neither was tested.

## Update

```bash
eve-wallets update           # download, verify and replace the binary
eve-wallets update --check   # only report whether a newer release exists
```

`update` (Linux and macOS) asks GitHub for the latest release, downloads the archive for your platform and `checksums.txt`, verifies the SHA-256, and replaces the running executable atomically, keeping the previous one as `eve-wallets.bak-prev` next to it. Then restart the service. A `dev` build (from source) refuses to update unless you pass `--force`. On Windows, `update` is not supported and prints manual steps; re-run `install.ps1` instead (or download the new zip, verify it and replace the binary). You can always update manually by downloading the archive as in [Install](#install).

`update` never touches the database.

### Backups and rollback

The schema is migrated automatically when the app opens the database, and a binary older than the database refuses to open it. So before it applies a migration, the new binary writes a consistent copy of the database next to it, `wallets.db.bak-v<N>`, where `<N>` is the schema version the database had (mode 0600; an existing backup of that version is never overwritten). If the backup cannot be written, the app stops with an error before touching the database.

To roll back: stop the app, restore the backup and the previous binary.

```bash
cd ~/.local/share/eve-wallets
rm -f wallets.db-wal wallets.db-shm
cp wallets.db.bak-v<N> wallets.db
mv ~/.local/bin/eve-wallets.bak-prev ~/.local/bin/eve-wallets
```

Data collected after the migration is lost by restoring. Existing wallets are linked to a user on the first collection cycle of a character whose token can read them, so after the upgrade to the sign-in version (schema v3) nobody sees old data until they sign in and that cycle runs.

## The page

The page shows a balance history chart with one line per selected wallet, an optional Total line, time ranges (24 h, 7 d, 30 d, All), a table of latest balances, and the result of the last collection. Each wallet panel (not the Total) has a Movements button that opens its journal (see [Movements](#movements)). `/api/status` also reports `journal_points`, the journal balances the last backfill saw (0 when it did not run); the page does not display it.

## Loyalty points

The page has a **Loyalty points** tab, the last tab of the tab bar (after the wallet owners), with, per character, a table of corporation logo, name and points (right aligned, with thousands separators, most points first). The tab loads `/api/loyalty` when first opened and again when reopened with data older than a minute, and the time-range card is hidden while it is active. The collector reads `GET /characters/{id}/loyalty/points` for every character whose token has the scope `esi-characters.read_loyalty.v1` and keeps only the latest snapshot per character and corporation in the `loyalty_points` table (schema v5, created in place); corporations ESI no longer returns are removed. Corporation names come from the public `POST /universe/names` (one batched call per cycle for the ids not cached yet) and are cached in `corporation_names`. When a lookup fails the page shows `Corp <id>`.

- **Existing characters must sign in again.** Tokens saved before this version lack the scope, so the collector records the skip `missing scope esi-characters.read_loyalty.v1` and the page shows a "Sign in again" notice for that character instead of a table (use "Add character" or the notice link and pick the character on the EVE login screen). ISK wallets, journals and renames keep working without the new scope.
- A 403 from ESI is the same skip; any other failure is an error of that character and the stored snapshot is kept.
- `GET /api/loyalty` (GET only, needs your session) returns `{"characters": [{"character_id", "character_name", "fetched_at", "needs_reauth", "corporations": [{"corporation_id", "name", "points"}]}]}` for your own characters only, corporations by points descending. `fetched_at` is unix seconds, `null` when nothing is stored. `needs_reauth` is true when the token lacks the scope, and its `corporations` list is empty.
### Loyalty history

ESI has no loyalty history, so it is recorded from now on in the append-only table `loyalty_history` (schema v6, created in place). In the same transaction as each snapshot replacement the collector appends a row `(character, corporation, taken_at, points)` only when the points differ from the last stored value of that pair (or there is none), so repeated collections store nothing. When ESI stops returning a corporation that had points, a single row with `0` is appended and no more follow until it changes again. Rows are removed with the character's token and survive a token move.

- It records from the first collection after you deploy this version. The migration seeds the history with the current `loyalty_points` values (`taken_at` = their `fetched_at`), so nothing already collected is lost, but earlier evolution cannot be recovered.
- `GET /api/loyalty/history?character_id=&corporation_id=&from=&to=&limit=&cursor=` (GET only, needs your session) returns `{"character_id", "points": [{"corporation_id", "name", "taken_at", "points"}], "next_cursor"}` ordered by `taken_at` and then `corporation_id` ascending. `character_id` is required and must be one of your own characters (otherwise 404); `corporation_id` is optional; `from` and `to` are inclusive RFC 3339 times; `limit` is 1 to 2000 (default 500). When more rows exist `next_cursor` is an opaque value to pass back as `cursor`; it is `null` on the last page. `taken_at` is unix seconds, names fall back to `Corp <id>`, and bad parameters answer 400. There is no page or chart for it yet.

- Not available: ESI has no endpoint for corporation loyalty points, and PLEX and event marks are not exposed by ESI as a wallet, so only character loyalty points are shown.

## Movements

The collector stores every wallet journal entry it downloads (date, signed amount in cents, type and description) in the `journal` table (schema v4, created in place without touching existing data). Each wallet panel has a **Movements** button that opens them newest first in a full-screen dialog sized to the window (title, filters and status on top, the table scrolling in the middle, a fixed **Previous** / **Next** pager at the bottom; Escape or **Close** returns focus to the button), with a type filter and a date range. On narrow screens (480 px or less) the Type column is hidden.

`GET /api/wallets/{id}/journal` (GET only, needs your session) returns `{"entries": [{"id", "date", "cents", "ref_type", "description"}], "next_cursor", "ref_types"}`. `date` is unix seconds and `cents` signed integer cents, like `/api/wallets`. Query parameters: `limit` (default 50, 1 to 200), `cursor` (the `next_cursor` of the previous page, a stable keyset on date and entry id, so new entries never shift a page), `ref_type`, and `from` / `to` (RFC 3339, inclusive). `next_cursor` is `null` on the last page and `ref_types` lists every type stored for the wallet. Invalid parameters answer 400; an unknown wallet or one you cannot see answers 404.

Backfill: the journal is only downloaded by the backfill (`serve` runs it every cycle, `backfill` once). Each pass fetches the full journal ESI still provides and inserts the entries not stored yet, so a wallet with no stored journal gets everything ESI still holds (about 30 days, up to 50 pages) on its first pass and later passes only add new entries. Running it twice inserts nothing new. A failed journal write is reported as an error and never changes the stored balances. With `--no-backfill` and no manual `backfill`, no movements are stored. ESI does not return entries older than its 30-day window, so earlier movements cannot be recovered.

## Wallet names

Corporation divisions are shown as `<corporation> · <name>` and the character wallet as the character name, in the picker, the latest balances table and the chart legend. The displayed name is, in order of precedence:

1. your label (source `custom`),
2. the division name reported by ESI (source `esi`),
3. the default: `Division N` for a corporation wallet, the character name for a character wallet (source `default`).

A label is never overwritten by a collection. Rename from the page (below) or from the CLI. The page shows a CLI change when you refresh it:

```bash
./eve-wallets wallets                       # find the wallet id
./eve-wallets label 3 Mining fund           # quotes are optional
./eve-wallets label --clear 3               # back to the ESI name or Division N
```

A name is 1-64 characters without control characters. `--db` goes before the wallet id, and an unknown or invalid id exits with code 1.

### Renaming from the page

Each corporation wallet panel has a **Rename** button. It opens a field with Save, Cancel and, when the wallet has your own name, **Reset name** (back to the ESI name or `Division N`). Saving an empty name also resets it. The new name shows in the panel title and the latest balances table without reloading the page.

These wallets cannot be renamed on the page, and the server refuses the request too (HTTP 403): character wallets, the corporation Master Wallet (division 1), and divisions whose name comes from ESI. A name is stored per wallet, so everyone who can see that wallet sees it.

The page calls `POST /api/wallets/{id}/label` with `{"name": "..."}`. It needs your session, a same-origin request (the same check as sign-out) and `Content-Type: application/json`. A wallet you cannot see answers 404, an invalid name 400. It returns `{"id", "name", "name_source"}`.

Division names come from ESI during collection. The login already requests the scope `esi-corporations.read_divisions.v1`, and ESI needs the in-game role Director for it. Without the scope nothing is requested. With the scope but without the Director role, the collection reports `missing Director role` as a skipped item (not an error). In both cases `Division N` or your labels are shown. Your own labels win over ESI names, so run `eve-wallets label --clear <wallet-id>` to see the ESI name of a division you already renamed. ESI only returns the divisions whose name is not the default, and the in-game default division names were not verified, so the fallback is `Division N`.

## How history works, and its limits

- Snapshots exist only while `serve` (or a manual or cron `collect`) runs. While it is off there are gaps.
- Every `serve` cycle (default 30 min, the first one right at start) also stores the journal balances of the last 30 days, so per-transaction detail is kept while `serve` runs. ESI only holds 30 days of journal: if more than 30 days pass without a backfill (`serve` or `backfill`), the detail of the older days is lost for good, and only snapshots remain for that gap.
- Backfill covers only the last 30 days (the ESI journal window). Balance points are stored only for journal entries that carry a balance; the movements list keeps every entry.
- ESI caching: wallet balance 2 min for characters and 5 min for corporations; journal 1 h. Rate limits: 150 tokens per 15 min for character wallets and 300 for corporation wallets (read from the ESI OpenAPI spec). On a rate limit the run is reported as partial and retried later; the journal backfill is skipped in a cycle whose snapshot was rate limited. If you hit rate limits, use `--no-backfill`.
- Money is stored as integer ISK cents, never floats. ESI amounts have up to four decimals (for example `3123652530.8712`); they are parsed exactly from the response text and rounded to the nearest cent (half away from zero) when stored.

## Security notes

- The server binds only to loopback and refuses other addresses. It also rejects requests whose `Host` is not a loopback name (DNS rebinding defense). It is not hardened for the internet and has no HTTPS.
- Refresh tokens are stored in plaintext in the SQLite file (directory 0700, file 0600), so anyone who can read that file can use them. They are never logged or returned by the API. To revoke the access, remove the application from the third-party applications page of your EVE account; also delete the database if you stop using the app.
- A rotated refresh token is saved before it is used.
- The page works offline: Chart.js is vendored and hash-verified (see `internal/web/static/VENDORED.md`).

## Architecture

- `cmd/eve-wallets`: CLI (`serve`, `collect`, `backfill`, `wallets`, `label`, `version`, `update`) and wiring.
- `internal/store`: SQLite (pure Go, `modernc.org/sqlite`) schema, migrations, users, tokens, sessions, wallet names and series queries.
- `internal/esi`: ESI client (ETag cache, wallets, journals, division names, loyalty points, universe names).
- `internal/sso`: EVE SSO client (PKCE, JWT validation) with the embedded client id and scopes.
- `internal/auth`: token source backed by the refresh tokens in the store.
- `internal/collector`: snapshot and backfill logic, graceful skips, wallet-to-user links.
- `internal/scheduler`: background collection loop.
- `internal/web`: sign-in, sessions, user-scoped JSON API and embedded page.

## Development

Releases are built by `.github/workflows/release.yml` when a `v*` tag is pushed; CI (`ci.yml`) runs gofmt, vet and tests.

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
- The add and move character flow was tried by the owner in a real browser against the real EVE SSO (header, adding a character, moving one from another user, signing in with an attached character); it is covered by fakes and structural tests, not by an automated browser test.
- The page (sign-in screen, header, sign out, session expiry, collecting state) in a real browser; its JavaScript has only structural tests and a syntax check.
- The migration of the real v2 database to v3 (it is covered by a test on a generated v2 file).
- The movements view and journal persistence are covered by fakes, a temporary SQLite database and literal-string UI checks only; they have not been run in a browser or against the real ESI.
- The `serve` cycle that backfills the journal every time (and `--no-backfill`) is covered by fakes only; it has not been run against the real ESI.
- The division names path was run against the real ESI only with a character that is not a Director. The case where a Director receives the names (stored with source `esi`, and cleared for divisions that return to the default name) is covered by fakes only.
- Loyalty points: the collection, the API and the page are covered by fakes, a temporary SQLite database and literal-string UI checks only; no real ESI call was made, the migration of the real database to v5 was not run, and the tab was not opened in a browser.
- Loyalty history: the migration to v6, the append-on-change logic and the API are covered by temporary SQLite databases and in-process HTTP tests only; the real database was not migrated and no real ESI call was made.
- The systemd unit above, and running on macOS or Windows at all (only linux/amd64 was built and run here; the other platforms were only cross-compiled). On a real Windows machine the installer ran and the binary starts, but `serve` first failed because the default database path needed `HOME`, which Windows does not set; that is fixed (see Default database), and the fix itself was only tested on Linux with a simulated OS. Nothing else about running on Windows is verified.
- `install.ps1` was only run on Linux under PowerShell 7, through its fake-release test (`scripts/test-install-ps1.sh`, also run in CI). It was not run on a real Windows machine nor in Windows PowerShell 5.1, so real file-lock behavior and the real GitHub redirect are unverified. The PATH option was exercised only through a test hook (`EVE_WALLETS_TEST_PATH_FILE`) that replaces the registry-backed user PATH, so the real user-scope registry write, the `[y/N]` prompt in a real console, and a new terminal picking the change up are unverified.
- The release workflow, `install.sh` and `eve-wallets update` against real GitHub releases: they were only tested against local fake releases.
