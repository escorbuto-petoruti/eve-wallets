# eve-wallets-total-movements

## Objective
The Total panel gets its own Movements view (table and daily income/expense chart) that aggregates the journals of all the wallets the Total covers, and the expanded Total chart gets the Movements button too.

## Problem / why
Movements exist per wallet only. The Total balance line sums several wallets, but its movements cannot be seen together.

## Decisions
- The Total covers the same wallets as its balance line (the section group's wallets, capped at `MAX_IDS`, same as `/api/series`).
- All entries are shown as stored: transfers between the owner's own wallets are NOT netted (they appear as an expense in one wallet and income in another).
- The table gets a Wallet column (wallet label) in Total mode only; the type filter lists the union of ref types; the daily chart sums all the wallets.
- New endpoints take `wallet_ids` like `/api/series`: `GET /api/journal` and `GET /api/journal/daily` (same query params and responses as the per-wallet ones, entries carry `wallet_id`). Every id must be visible to the user, otherwise 404 (same rule as the per-wallet endpoints).
- Cursor stays keyset: `<date>-<id>-<wallet_id>` for the multi-wallet endpoint (entry ids are only unique per wallet); the per-wallet cursor format is unchanged.

## Constraints
- Artifacts in English; conventional commits; no AI attribution lines.
- ~400 authored changed lines per task is a planning heuristic only.

## Tasks
- [x] T1 backend: store multi-wallet journal and amounts queries (wallet id set, tie-break by wallet id), `GET /api/journal` and `GET /api/journal/daily`, wallet_id in entries, tests (visibility 404, cursor paging across wallets with colliding entry ids, filters, daily sums across wallets, zero-fill, tz).
  - Evidence: RED = new store and web tests failed to compile / returned 404 before the change; GREEN = `go test ./internal/store ./internal/web` pass after it. Commit: "feat(web): journal and daily totals across several wallets". Route: delegated writer.
- [x] T2 UI: Movements button on the Total panel and on the expanded Total chart; movements controller in Total mode (title, Wallet column, URLs, types, daily chart); structural UI test; README.
  - Evidence: RED = TestAppHasMovementsView, TestAppHasMovementsDailyChart and TestAppHasExpandedChart failed on the literal strings after the JS change; GREEN = `go test ./internal/web` pass after updating them and adding TestAppHasTotalMovements; `node --check` ok. Not run in a browser. Commit: "feat(web): movements for the Total panel". Route: delegated writer.

## Routing / test policy
- Test-first for Go behavior (RED before GREEN); JS covered by literal-string UI tests plus `node --check`.
- One delegated writer, one work-unit commit per task on `feat/total-movements`.
- Delivery: forecast about 400-450 authored lines (borderline); single PR unless the running count clearly exceeds the budget, then ask the owner for a stacked split. PR through the escorbuto-petoruti token.

## Progress
- Mapped earlier: per-wallet journal `internal/web/journal.go`, store `internal/store/journal.go`, `buildMovements` in `internal/web/static/app.js`, series total `internal/web/total.go` and `/api/series`.
