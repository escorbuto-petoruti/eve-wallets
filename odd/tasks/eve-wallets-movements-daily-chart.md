# eve-wallets-movements-daily-chart

## Objective
In a wallet's Movements dialog, show a bar chart with the total income and total expenses per day.

## Problem / why
The dialog only lists journal rows 50 at a time, so the owner cannot see at a glance how much came in and went out each day. The totals must cover the whole filtered range, not the visible page, so they are computed by the server.

## Decisions (owner)
- The chart follows the dialog filters (type, from, to).
- Days are grouped in the browser's local time zone (like the existing date filters).
- With no date filter, the chart shows the last 30 days (ESI only keeps about 30 days of journal anyway).

## Design
- `GET /api/wallets/{id}/journal/daily?ref_type=&from=&to=&tz=<IANA zone>` (same visibility rule as the journal: unknown or foreign wallet is 404). Returns `{"days":[{"day":"YYYY-MM-DD","income_cents":N,"expense_cents":N}]}`, ascending, zero-filled between the first and last day, expenses as positive magnitudes. Bad `tz` or bad range is 400.
- Chart.js 4.5.1 is already vendored (`internal/web/static/chart.umd.min.js`); no new dependency. Server text goes through textContent only; no inline scripts (CSP of the page stays).
- Chart colors follow the existing gain/loss palette and light/dark themes (dataviz skill).

## Constraints
- Artifacts in English; conventional commits; no AI attribution lines.
- ~400 authored changed lines per task is a planning heuristic only.

## Tasks
- [x] T1 store + endpoint: daily totals query, handler, route, tests (store with a temp SQLite DB, handler incl. 404, tz, filters, zero-fill, DST day).
- [x] T2 UI: chart in the movements dialog (updates on Apply, respects filters, accessible summary), CSS, structural UI test, README Movements section and not-verified bullet.
- [x] T3 review fixes: (a) from absent now defaults to 30 days before to/now (commit 2436dd3); (b) daily chart retries once without tz on a 400 (commit below in git log).

## Routing / test policy
- Test-first when a runnable deterministic test exists; RED observed before GREEN.
- One delegated writer (writer trigger), one work-unit commit per task on `feat/movements-daily-chart`.
- Delivery: forecast about 350 authored changed lines, under the 400 budget; single PR, push/PR stays the owner's decision.

## Progress
- Mapped: journal endpoint `internal/web/journal.go`, route `internal/web/web.go:89`, dialog `buildMovements` in `internal/web/static/app.js`, store `internal/store/journal.go`.

- T1 done: commit d552187. RED: store/web tests failed (undefined JournalAmounts, 404 route) before code; GREEN: gofmt/build/vet/test/race/node --check clean.
- T2 done (commit below in git log). Pure JS: literal-string UI test + node --check; RED not observed for JS; not run in a browser. dataviz validator not run (reused --good/--bad tokens).
- T3 done. (a) RED: TestJournalDailyBoundsWindowWhenOnlyToIsGiven returned income 23 instead of 18 before the fix, GREEN after. (b) literal-string UI test + node --check; RED not observed for JS; not run in a browser.
