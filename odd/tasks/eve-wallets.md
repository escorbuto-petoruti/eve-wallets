# eve-wallets

## Objective
Local Go web app that shows, with charts, the evolution of every wallet of every authenticated EVE character: the personal wallet and the corporation wallet divisions the character can read.

## Problem / why
ESI exposes only the current balance (`/wallets`) and a 30-day journal, so history must be snapshotted by the app. Journal entries carry a running `balance`, which allows backfilling the first 30 days.

## Decisions
- Go. SQLite through `modernc.org/sqlite` (pure Go; this environment has no C compiler). Chart.js vendored and embedded (works offline). Simple server-rendered page plus a JSON API.
- Tokens: a `TokenSource` that shells out to the `eve-auth` binary (`eve-auth token <id>`, `eve-auth list`). Path configurable with `EVE_AUTH_BIN`; `EVE_CLIENT_ID` is passed through. This app never stores tokens.
- Snapshots: background loop inside `eve-wallets serve` (default every 30 min) plus a manual `eve-wallets collect`.
- Money is stored as integer ISK cents, never floats.
- ESI facts (verified in the OpenAPI spec): base `https://esi.evetech.net`; required header `X-Compatibility-Date: 2020-01-01`; character wallet scope `esi-wallet.read_character_wallet.v1` (no roles); corporation wallet scope `esi-wallet.read_corporation_wallets.v1` and role Accountant or Junior_Accountant; journal is 30 days back and paginated with `X-Pages`; rate limits 150 tokens/15 min (character) and 300 tokens/15 min (corp). Honor ETag/Cache-Control.

## Constraints
- Module `github.com/escorbuto-petoruti/eve-wallets`. Layout: `cmd/eve-wallets`, `internal/store`, `internal/esi`, `internal/auth`, `internal/collector`, `internal/web`.
- No tokens or secrets in logs, DB or the repo. DB file ignored by git.
- A character without the corp role must degrade gracefully (skip the corp wallet, record why), never abort the whole collection.
- Out of scope for the MVP: multi-user auth, deployment, alerts, transactions endpoint, division names.

## Tasks
- [x] T1 module scaffold + `internal/store` (SQLite schema, snapshots as integer cents, migrations, queries for series) + tests
- [x] T2 `internal/esi` client (headers, ETag, wallet balance, journal with pagination, character to corporation id) + httptest tests
- [x] T3 `internal/auth` TokenSource over `eve-auth` + `internal/collector` (snapshot all characters, graceful corp-role failure) + tests
- [ ] T4 `internal/web` + `cmd/eve-wallets`: `serve` (JSON API, embedded Chart.js page, background loop) and `collect`
- [ ] T5 journal backfill of the last 30 days (idempotent, deduplicated)
- [ ] T6 README: setup (eve-auth install, scopes, login), usage, limits

## Routing / test policy
- Test-first with `go test ./...`; ESI and `eve-auth` faked. One delegated writer per task, one Conventional Commit per task on the feature branch.
- Native review boundary: branch point (initial commit on `main`).

## Progress
Branch `feat/eve-wallets-mvp`, initial commit on `main` (local, not pushed). T1 done (delegated writer, test-first: RED observed on undefined symbols, then GREEN). `internal/store` with versioned migrations (`PRAGMA user_version`), WAL, FKs, partial unique indexes for journal/snapshot idempotency. Evidence: `CGO_ENABLED=0 go vet ./...` clean; `go test ./...` ok; `go test -count=10 ./...` ok; `gofmt -l .` empty. Commit: `feat(store): add SQLite wallet balance store`.

T2 done (delegated writer, test-first: RED observed on undefined symbols, then GREEN). `internal/esi`: `Client` with `CharacterWallet`, `CorporationWallets`, `CharacterJournal`, `CorporationJournal` (all pages, cap 50), `CharacterCorporationID`; exact cents via `ParseCents` (no float; exponent and sub-cent precision rejected); `*APIError`, `*RateLimitError` (420/429, Retry-After), `IsForbidden`/`IsNotFound`; ETag cache keyed by URL + token hash, 304 reuse, errors never cached; token scrubbed from errors. Evidence: `CGO_ENABLED=0 go vet ./...` clean; `go test ./...` ok; `go test -count=10 ./...` ok; `gofmt -l .` empty. Commit: `feat(esi): add ESI wallet client`.

T3 done (delegated writer, test-first: RED observed on undefined symbols in both packages, then GREEN). `internal/auth`: `TokenSource`, `EveAuth` (injectable `Runner`, no shell, bounded and scrubbed stderr, token validated and never in errors, per-call timeout). `internal/collector`: `New(Deps).Run` with shared truncated `takenAt`, scope-based skips, 403 to `missing corporation role`, one snapshot per corp per run, per-item errors, rate-limit partial report (`RateLimited`, `RetryAfter`), ctx cancel. `internal/esi`: added `CorporationName`. Evidence: `CGO_ENABLED=0 go vet ./...` clean; `go test ./...` ok; `go test -count=10 ./...` ok; `gofmt -l .` empty. Commit: `feat(collector): collect wallet snapshots through eve-auth`.

## Next step
T4: `internal/web` + `cmd/eve-wallets` (`serve`, `collect`).
