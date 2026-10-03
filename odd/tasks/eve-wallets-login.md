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
- [x] T2 store v3 migration + API: users, tokens, user_wallets, sessions; migration test from a v2 file. Evidence: migration 3 in `internal/store/store.go` (+ `wallets`/`latestBalances`/`series` private helpers shared by the scoped reads); `internal/store/users.go` (`UpsertUser`, `SaveToken`/`GetToken`/`Tokens`/`DeleteToken`, `LinkWallet`, `CreateSession`/`SessionUser`/`DeleteSession`/`PurgeExpiredSessions`, `WalletsForUser`/`LatestBalancesForUser`/`SeriesForUser`); `Token.UpdatedAt` is set by the caller (zero means now); foreign keys already enforced per connection via DSN pragma (test proves it on several pooled connections); `internal/store/users_test.go` RED (undefined: SaveToken, then schema v2 / no such table) then GREEN; go build, go vet, gofmt -l clean, go test ./... ok, -race ok, -count=5 ok. Route: delegated writer.
- [ ] T3 SQLite-backed `TokenSource` (refresh with rotation persisted first) + collector links wallets to users; wiring in `main.go`.
- [ ] T4 web: `/auth/login`, `/auth/callback`, `/auth/logout`, session middleware, `/api/*` scoped to the session user, 401 when signed out.
- [ ] T5 UI: signed-out login page, signed-in header with character name and logout, `app.js` handles 401.
- [ ] T6 remove the eve-auth shell-out and its env vars, README, live check by the owner.

## Routing / test policy
- Test-first with `go test ./...`; ESI and SSO faked (hand-written fakes, `httptest`, `store.Open(t.TempDir()...)`). One delegated writer per task, one Conventional Commit per task on `feat/web-login`. `go test -race` is now available (gcc installed).
- Route declaration: T1-T6 each touch 2+ non-trivial files, so each runs as delegated direct (writer trigger). Mapping was done by one read-only explorer.
- Native review boundary for assessments: 68c9d0c (T1 reviewed and acknowledged).

## Progress
T1 done: SSO package embedded with default wallet configuration (68c9d0c).
T2 done: store schema v3 with users, tokens, per-user wallet links, sessions and user-scoped reads.

## Next step
T3.
