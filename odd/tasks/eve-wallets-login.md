# eve-wallets-login

## Objective
Let a person sign in to the eve-wallets web page with EVE SSO. The signed-in character is the app user and sees only the wallets that character can read. This is phase 1; phase 2 (a signed-in user registers more of their own characters) is a separate feature.

## Problem / why
The web is read-only, has no sessions and shows every wallet in the database. Tokens come from the external `eve-auth` CLI with a shared `EVE_CLIENT_ID` env var. The app is meant to be shared as a LOCAL install (127.0.0.1 only, own SQLite file per person), so it cannot depend on a second binary or on each person registering an EVE application. A user identity is also the base for managing things from the web later.

## Decisions
- Local install only, loopback only. No HTTPS, no hardening for the internet. Cookies are `HttpOnly`, `SameSite=Lax`; no `Secure` flag because the origin is plain http on localhost.
- Embedded client id, the project owner's application (PKCE public client, not a secret). Fixed redirect `http://localhost:8088/auth/callback`. The owner must register that redirect URI at developers.eveonline.com; this was NOT verified from here.
- Any EVE character may sign in; no allowlist. Login requests the wallet scopes (character wallet, corporation wallets, corporation divisions) so signing in also registers the character for collection.
- SSO code is copied from eve-auth `internal/sso` (about 400 lines, only dependency `golang-jwt/jwt/v5`) into eve-wallets `internal/sso`. eve-auth's repo is private/unknown visibility, so importing it as a module would break `go install` for other people. Copy now; extract to a shared public module only if the duplication hurts.
- Tokens live in the eve-wallets SQLite file (new `tokens` table, DB dir is 0700 and file 0600). A rotated refresh token is persisted before it is used (lesson from eve-auth).
- Ownership model (store v3): `users(character_id, name)`, `tokens(character_id, refresh_token, scopes, ...)`, `user_wallets(user_id, wallet_id)`, `sessions(id_hash, user_id, expires_at)`. A wallet is visible to a user through `user_wallets`; a corporation wallet can be linked to several users. Existing wallets get linked on the first collection cycle of the user whose token can read them, so no manual data migration.
- Session id is 32 random bytes in the cookie, only its hash is stored. Logout is a POST with an Origin check; the web `guard` keeps rejecting every other non-GET/HEAD request.
- The `auth.TokenSource` interface stays; a SQLite-backed implementation replaces the `eve-auth` shell-out. The `EveAuth` runner and the `EVE_AUTH_BIN`/`EVE_CLIENT_ID` env vars are removed in the last task.
- `collect`, `backfill` and `serve` background cycles keep working for every registered character; the `/api/*` endpoints filter by the session user.

## Constraints
- No tokens in output, logs or API responses. API stays GET/HEAD except `POST /auth/logout`.
- Existing behavior for current data must not break: the real DB (v2, ~7.7k balances) migrates in place; a backup exists at `wallets.db.bak-v1` and a new backup must be taken before the v3 migration on the real file.
- About 400 authored changed lines per task is only a planning heuristic.

## Tasks
- [x] T1 `internal/sso` copied from eve-auth (+tests), embedded client id, fixed redirect, wallet scopes. Evidence: client/config/jwks/pkce/validate.go and their tests + helpers_test.go copied unchanged (no module-path edits needed); `golang-jwt/jwt/v5 v5.3.1` added; `internal/sso/defaults.go` (`DefaultClientID`, `DefaultRedirectURL`, `WalletScopes()`, `DefaultConfig()`); DefaultConfig tests RED (undefined: DefaultConfig) then GREEN; go build, go vet, gofmt -l clean, go test ./... ok, -race ok, -count=5 ok. Commit: 68c9d0c. Native review: assessed medium (go.mod), consent granted, approved, acknowledged (lineage review-d31766c813c7adc9); reviewed boundary now 68c9d0c. Informational follow-ups, none blocking (inherited from eve-auth's copy): R3-001 `internal/sso/jwks.go:26-42` jwksCache holds the mutex during the network fetch and every unknown kid refetches; R3-002 `internal/sso/client.go:71` token() accepts a response without expires_in (ExpiresIn 0) and does not bound negative/huge values; R3-003 `internal/sso/jwks.go:54-56` failure paths of fetch have no tests. Route: delegated writer.
- [x] T2 store v3 migration + API: users, tokens, user_wallets, sessions; migration test from a v2 file. Evidence: migration 3 in `internal/store/store.go` (+ `wallets`/`latestBalances`/`series` private helpers shared by the scoped reads); `internal/store/users.go` (`UpsertUser`, `SaveToken`/`GetToken`/`Tokens`/`DeleteToken`, `LinkWallet`, `CreateSession`/`SessionUser`/`DeleteSession`/`PurgeExpiredSessions`, `WalletsForUser`/`LatestBalancesForUser`/`SeriesForUser`); `Token.UpdatedAt` is set by the caller (zero means now); foreign keys already enforced per connection via DSN pragma (test proves it on several pooled connections); `internal/store/users_test.go` RED (undefined: SaveToken, then schema v2 / no such table) then GREEN; go build, go vet, gofmt -l clean, go test ./... ok, -race ok, -count=5 ok. Commit: f6db4bf. Native review: assessed medium (executable_change in `internal/store/names_test.go`), consent granted, approved, acknowledged (lineage review-2bfd3d9ce48b7987); reviewed boundary now f6db4bf. Two informational SUGGESTIONS, not blocking and their claims were not captured: `internal/store/users.go:47` (SaveToken) and `internal/store/users.go:76` (token row scan). Route: delegated writer.
- [x] T3 SQLite-backed `TokenSource` (refresh with rotation persisted first) + collector links wallets to users; wiring in `main.go`. Evidence: `internal/auth/tokens.go` (`StoreTokens`, `TokenStore`/`Refresher` interfaces, `ErrReauthRequired`/`ReauthError`, per-character mutex, access token cached until 60 s before expiry, rotated refresh token saved before the access token is returned), `auth.Character.UserID`; collector `StoreWriter.LinkWallet`, walker links the current character's user for `Run` and `Backfill`, and remembers corporation wallet ids so a second user of an already collected corporation is linked without a second ESI fetch; `main.go` `newTokens` now takes the opened store (`newStoreTokens` = `StoreTokens` + `sso.NewClient(sso.DefaultConfig())`), `EveAuth` code kept but unwired, usage no longer lists `EVE_AUTH_BIN`/`EVE_CLIENT_ID`, `collect`/`backfill` with no registered characters exit 1 with a hint to `eve-wallets serve` and sign in. Tests RED (auth: undefined StoreTokens/ErrReauthRequired; collector: 6 link tests failing; cmd: harness no longer compiled against the old `newTokens(bin)`) then GREEN: go build ./..., go vet ./..., gofmt -l . (empty), go test ./... ok, go test -race ./internal/auth/... ./internal/collector/... ./cmd/... ok, go test -count=5 ./internal/auth/... ./internal/collector/... ok. Route: delegated writer (writer trigger: 2+ non-trivial files). Open points: a character with the corporation scope but not the role is linked to an already collected corporation without a fetch (the spec asked for no second fetch); invalid_grant is detected by string match because `internal/sso` has no typed error; README still describes eve-auth (T6). Commits: feat 44375b8; fix c54d68b (corporation role verified before linking another user); fix a8d277b (rotated refresh token kept in memory when saving fails). Native review: assessed HIGH (authentication, `internal/auth/auth.go`), consent granted, 4 lenses, one CRITICAL candidate-caused finding R4-001 (`internal/auth/tokens.go:136-141`, resilience: rotated refresh token lost when SaveToken fails) fixed in a8d277b (103 changed lines of the 200 budget), targeted validation passed, approved and acknowledged (lineage review-41fd3cac727c4305); reviewed boundary now a8d277b. The 16 informational findings that did not block are listed under Follow-ups.
- [x] T4 web: `/auth/login`, `/auth/callback`, `/auth/logout`, session middleware, `/api/*` scoped to the session user, 401 when signed out. Evidence: `internal/web/auth.go` (SSO interface, single-use in-memory login flows bounded to 64 with 10 min expiry, `eve_login` cookie binding the flow to the browser, callback that stores the user and refresh token before creating the session, 32-byte session cookie with only its SHA-256 stored, logout with Origin/Referer same-host check, session middleware, `requireUser`, Host allow-list); `internal/web/web.go` (`Deps` gains SSO, Now, OnLogin, AllowedPort; `/api/wallets` and `/api/series` use the `*ForUser` store methods; guard allows POST only on `/auth/logout`); `internal/scheduler/scheduler.go` `Loop.Trigger()` (buffered 1, coalesces, never overlaps, ignored while rate limited); `cmd/eve-wallets/main.go` (`deps.newSSO`, `OnLogin` triggers the loop, no-op with `--no-collect`, one-line stderr warning when the port is not 8088, Host port taken from the listener). Tests RED (scheduler: `l.Trigger undefined`; cmd: `undefined: ssoPortWarning`; web: tests written before `auth.go` and `Deps.SSO` existed, package did not compile) then GREEN: go build ./..., go vet ./..., gofmt -l . (empty), go test ./... ok, go test -race ./internal/web/... ./internal/scheduler/... ./cmd/... ok, go test -count=5 ./internal/web/... ./internal/scheduler/... ok. Commits: scheduler Trigger bf3b234; web login and scoped API 4d13ce4. Native review: assessed HIGH (authentication in `internal/web/auth.go`), consent granted, 4 lenses, approved with no correction, acknowledged (lineage review-bea6de55bb6034c3); reviewed boundary now 4d13ce4. 12 informational findings are listed under Follow-ups. Route: delegated writer. The static page still called the API unauthenticated and got 401 until T5.
- [x] T5 UI: signed-out login page, signed-in header with character name and logout, `app.js` handles 401. Evidence: `index.html` (`#signed-out` with the `/auth/login` anchor and `#session-message`, `#signed-in` wrapper, `#user-bar` with name and `<form method="post" action="/auth/logout">`, `#collecting` card with `aria-live`), `app.js` (boot via `/api/me`; `getJSON` turns any 401 into the signed-out view with "Your session expired. Sign in again." when it was signed in, stops polling and drops stale responses by epoch; empty state polls `/api/wallets` + `/api/status` with 5 s start, x1.5 backoff capped at 30 s, 40 tries, and reports status errors/skips), `style.css` (`[hidden]`, top bar, `a.button`). New `internal/web/ui_test.go`; `web_test.go` empty-state hint updated. Tests RED (index lacks signed-out/signed-in/form, app.js lacks /api/me and 401) then GREEN: go build ./..., go vet ./..., gofmt -l . (empty), go test ./... ok, go test -race ./internal/web/... ok, go test -count=5 ./internal/web/... ok, node --check internal/web/static/app.js ok. NOT verified: rendering and behavior in a real browser (no JS runtime tests exist; only structural Go tests and a syntax check). Commit: e47e00a. Native review: assessed medium (executable_change in `internal/web/static/app.js`), 299 changed lines, review not due (`under_budget`) at the time; later covered together with T6 by one base-diff review of `4d13ce4..0ecc7ee`, approved and acknowledged (lineage review-6e67b907bb46ba7e); reviewed boundary now 0ecc7ee. Route: delegated writer.
- [x] T6 remove the eve-auth shell-out and its env vars, update the README. Evidence: `internal/auth/auth.go` reduced to `Character` and `TokenSource` (removed `EveAuth`, `NewEveAuth`, `Runner`, `Options`, `Result`, exec plumbing, `defaultBin`, stderr scrubbing, list/token parsers); `internal/auth/auth_test.go` deleted (it only covered the shell-out); `TestUsageNoLongerMentionsEveAuth` removed from `cmd/eve-wallets/main_test.go` (main.go already had no eve-auth code or env handling after T3); `names.go` comment fixed; README rewritten against the code (sign-in, scopes from `sso.WalletScopes`, fixed redirect and port 8088, 7-day sessions, per-user visibility, collection after login, token storage, v3 migration backup advice, systemd example marked untested, not-verified list). Checks: go build ./..., go vet ./..., gofmt -l . (empty), go test ./... ok, go test -race ./... ok, go test -count=3 ./... ok; `rg -n -i "eve-auth|EVE_AUTH_BIN|EVE_CLIENT_ID|EveAuth" . --glob '!odd/**'` leaves only the README sentence that says eve-auth is no longer needed and three error-string fixtures in `internal/collector/*_test.go` (outside T6's edit surface; harmless). Native review: T5+T6 were assessed together as one base-diff range `4d13ce4..0ecc7ee` (872 changed lines, tier high, executable_change in `internal/web/static/app.js`), 4 lenses, approved with no correction and acknowledged (lineage review-6e67b907bb46ba7e); reviewed boundary now 0ecc7ee. 12 informational findings are listed under Follow-ups. Route: delegated writer.
- [ ] T7 live check by the owner: register `http://localhost:8088/auth/callback` at developers.eveonline.com for the embedded client id; stop the running systemd service; back up `~/.local/share/eve-wallets/wallets.db` (v2) before the first run of the new binary (an older binary refuses a DB with a newer schema version, so the old service cannot be restarted on a migrated DB); install the new binary; sign in; confirm the page shows the owner's wallets and the corporation skip; confirm logout and session expiry behavior in a real browser.

## Routing / test policy
- Test-first with `go test ./...`; ESI and SSO faked (hand-written fakes, `httptest`, `store.Open(t.TempDir()...)`). One delegated writer per task, one Conventional Commit per task on `feat/web-login`. `go test -race` is now available (gcc installed).
- Route declaration: T1-T6 each touch 2+ non-trivial files, so each runs as delegated direct (writer trigger). Mapping was done by one read-only explorer.
- Native review boundary for assessments: 0ecc7ee (T1 to T4 reviewed and acknowledged at 4d13ce4; T5+T6 reviewed together as `4d13ce4..0ecc7ee`, 872 lines, tier high, acknowledged as lineage review-6e67b907bb46ba7e).

## Progress
T1 done: SSO package embedded with default wallet configuration (68c9d0c).
T2 done: store schema v3 with users, tokens, per-user wallet links, sessions and user-scoped reads (f6db4bf).
T3 done: SQLite-backed token source with rotation persisted first, collector links wallets to users, `collect`/`backfill`/`serve` wired to it.
T3 follow-up: the writer found a role gap in T3 (a second user was linked to an already collected corporation without a role check, so corporation wallet data could leak between characters of the same corporation). Fixed in the follow-up commit `fix(collector): verify the corporation role before linking another user`: the second user's character now proves access with its own token via `CorporationWallets`; a 403 records `missing corporation role` and links nothing.

T4 done: EVE SSO sign-in, sessions and the user-scoped API; `serve` triggers a collection right after a login.
T5 done: sign-in screen, session header with sign out, 401 handling and the collecting empty state (static assets only; not verified in a real browser) (e47e00a).
T6 done: the eve-auth shell-out, its env vars and tests are gone; README rewritten for the web sign-in.
T5/T6 review closed on 2026-10-03: one base-diff review of `4d13ce4..0ecc7ee` (872 changed lines, tier high, 4 lenses) came back approved with 12 informational findings and no BLOCKER/CRITICAL, and was acknowledged as lineage `review-6e67b907bb46ba7e`; the frozen candidate tree equalled HEAD (`0ecc7ee`) at acknowledgement, so the approval still matches the tree. The native transaction record is cleaned up when its authority is burned, so the finding locations are not listed here.
Remaining: T7, the live check by the owner (SSO redirect registration, DB backup, real browser).

## Follow-ups
Informational findings from the T3 native review that did not block (claims were not captured; re-review only if the user asks):
- R1-001 `internal/auth/tokens.go:128`
- R1-002 `internal/auth/tokens.go:136-140`
- R1-003 `internal/collector/collector.go:51-52`
- R2-001 `internal/collector/collector.go:241`
- R2-002 `internal/collector/collector.go:343`
- R2-003 `internal/collector/collector.go:294`
- R2-004 `internal/auth/auth.go:35-36`
- R2-005 `cmd/eve-wallets/main.go:140`
- R2-006 `internal/auth/tokens.go:153`
- R2-007 `internal/auth/tokens.go:105`
- R2-008 `internal/collector/links_test.go:314`
- R3-001 `internal/auth/tokens.go:132-141`
- R3-002 `internal/auth/tokens_test.go:244-264`
- R3-003 `internal/auth/tokens.go:104-109`
- R4-002 `internal/collector/collector.go:348-349`
- R4-003 `internal/collector/collector.go:357-359`

Informational findings from the T4 native review that did not block (claims were not captured; re-review only if the user asks):
- R1-cookie-scope `internal/web/auth.go:196-199`
- R1-login-flow-eviction `internal/web/auth.go:82-91`
- R2-auth-errnames `internal/web/auth.go:118-119`
- R2-auth-test-branch `internal/web/auth_test.go:236-237`
- R3-001 `internal/scheduler/scheduler.go:75-84`
- R3-002 `internal/web/auth_test.go:254-263`
- R3-003 `internal/web/auth.go:82-91`
- R3-004 `internal/web/auth.go:247-252`
- R4-001 `internal/web/auth.go:212-217`
- R4-002 `internal/web/auth.go:248-252`
- R4-003 `internal/web/auth.go:73-90`
- R4-004 `internal/scheduler/scheduler.go:76-89`

Informational findings from the T5+T6 native review (lineage `review-6e67b907bb46ba7e`, acknowledged; the transaction record is cleaned up on closure, so only ids and severities are recorded, without locations):
- review-resilience: R4-001 WARNING
- review-readability: R2-001 WARNING, R2-002 SUGGESTION, R2-003 SUGGESTION, R2-004 SUGGESTION, R2-005 WARNING, R2-006 SUGGESTION, R2-007 SUGGESTION, R2-008 SUGGESTION
- review-reliability: R3-001 WARNING, R3-002 WARNING, R3-003 WARNING

The earlier T1 follow-ups (R3-001..R3-003 in `internal/sso`) are listed on the T1 task above.

## Next step
T7 (owner): live check, including the SSO redirect registration, the DB backup before the first run and a real-browser pass over the T5 UI.
