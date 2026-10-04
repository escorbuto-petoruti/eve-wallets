# eve-wallets-loyalty-points

## Objective
Show the loyalty points (LP) of each signed-in user's characters, per issuing corporation.

## Problem / why
The game wallet window also lists LP. Verified against the ESI OpenAPI spec and a live call: LP is readable for characters only (`GET /characters/{id}/loyalty/points`, scope `esi-characters.read_loyalty.v1`, returns `corporation_id` + `loyalty_points`); there is no corporation LP endpoint. PLEX and event marks have no wallet endpoint and do not appear in assets, so they are out of scope.

## Decisions
- Character LP only. Store the latest snapshot per (character, corporation) with its fetch time; no history for now.
- Corporation names come from the public `POST /universe/names`, cached in the database; a failed name lookup falls back to the corporation id.
- The new scope is added to the requested sign-in scopes. Existing tokens lack it: the collector records a graceful skip ("missing scope") and the UI tells the user to sign the character in again, like the existing re-authorization notice. Nothing breaks for ISK wallets.
- Read-only API scoped to the signed-in user's own characters.

## Constraints
- CSP `default-src 'self'; img-src 'self' https://images.evetech.net`; no inline scripts/styles; server text only via `textContent`; UI tests are literal-string checks (no innerHTML, no `.style.`).
- Existing databases migrate in place (read the current user_version from the code). No new dependencies.

## Tasks
- [x] T1 sso + esi + store + collector: scope, `CharacterLoyaltyPoints`, `UniverseNames`, migration, persist snapshot, graceful skip when the scope is missing + tests (RED first)
- [x] T2 API: `GET /api/loyalty` (per-user, characters only) + tests (RED first)
- [x] T3 UI + README: Loyalty points view per character with corporation logo and name, sorted by points, re-sign-in hint when the scope is missing + tests

## Acceptance
- A character that granted the LP scope shows its LP per corporation; one that did not shows a clear re-sign-in message and its ISK wallets still work; other users' characters are never exposed.

## Progress / evidence
- T1 (97be892): scope `esi-characters.read_loyalty.v1` added to `sso.WalletScopes()` (optional: the collector only skips LP, never ISK); `esi.CharacterLoyaltyPoints` and `esi.UniverseNames` (batches of 1000, deduplicated, public POST); migration 5 (`loyalty_points` with FK to `tokens` ON DELETE CASCADE, `corporation_names`); store `ReplaceLoyalty` (transactional), `LoyaltyForUser`, `UpsertCorporationNames`, `CorporationNames`, `ScopesForUser` (the last is used by T2); collector fetches LP in `Run` only (not Backfill), one batched name lookup per run, missing scope or 403 is the skip `missing scope esi-characters.read_loyalty.v1`, other errors are item errors, a rate limit stops the run. RED observed: esi and store tests failed to compile (undefined methods), sso test failed on the scope list, 9 new collector tests failed on assertions. 10 existing collector/cmd tests were adapted (the new skip adds one entry for tokens without the scope; fixtures got the scope or the expected skip count rose by one). Route: delegated writer.
- T2 (5140aaa): `GET /api/loyalty` via `requireUser` (guard keeps GET/HEAD only), own characters only (`CharactersForUser` + `ScopesForUser` + `LoyaltyForUser`), corporations by points descending, name fallback `Corp <id>`, `needs_reauth` with an empty list when the token lacks the scope, `fetched_at` null when nothing is stored. RED observed: 3 new web tests failed (404 before the route existed). Route: delegated writer.
- T3 (6d688f3): Loyalty points card (`#loyalty-card`, polite live `#loyalty-status`) beside the owner tabs, which are untouched; per-character portrait, corporation table with logo, name and points (`.num`), reauth notice reusing `.reauth-notice` and `/auth/add-character`, empty and error states, cleared on sign-out, reloaded when polling finds the first data. Server text only via textContent. README documents the feature, the scope, the re-sign-in need and what ESI does not offer. RED observed: 3 new literal-string UI tests failed before the markup, JS and CSS existed. Route: delegated writer.
- Final checks (after T3): `gofmt -l .` empty; `go vet ./...` clean; `go test -count=1 ./...` all packages ok; `go test -count=5 ./internal/store/... ./internal/collector/... ./internal/web/...` ok; `node --check internal/web/static/app.js` ok.
- Not verified: no browser run of the card, no real ESI call (scope grant, `/loyalty/points` or `/universe/names` against the live service), no run against the real database (migration 5 is covered by a generated v4 file), and the LP value is rendered through a JS number so points above 2^53 would lose precision.
