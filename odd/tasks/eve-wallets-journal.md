# eve-wallets-journal

## Objective
Browse the movements (wallet journal) of each wallet in the web UI, with a one-time backfill of everything ESI still provides.

## Problem / why
The collector already downloads the journal (`esi.JournalEntry`: id, date, amount, running balance, ref type, description) but only keeps `balances` rows (time, balance, journal_ref). Amount, type and description are discarded. ESI limits journal history, so persisting early loses less.

## Scope
- Store: new migration with a `journal` table, unique per (wallet, ESI entry id), idempotent inserts.
- Collector: persist every journal entry during normal runs and run a one-time backfill for wallets whose journal was never stored (re-fetch all available pages, insert missing rows, no duplicates, no change to existing `balances`).
- API: `GET /api/wallets/{id}/journal` paginated (cursor or offset, bounded page size), filters by ref type and date range; only wallets the user can see (404 otherwise).
- UI: open a wallet panel's movements as a paginated list with type and date filters; text via `textContent`; CSP unchanged.

## Constraints
- Money stays integer cents. No new dependencies. CSP `default-src 'self'`; no inline scripts/styles; UI tests are literal-string checks (no innerHTML, no `.style.`).
- Existing databases migrate in place without data loss (current user_version must be read from the code).
- API stays read-only for this feature (GET only).

## Tasks
- [x] T1 store + collector: migration, persist journal, one-time backfill + tests (RED first)
- [x] T2 API: journal endpoint with pagination, filters, per-user access + tests (RED first)
- [x] T3 UI + README: movements list with filters and pagination + tests

## Acceptance
- A user can open any wallet they see and page through all stored movements with type/date filters; backfill is idempotent (running twice inserts nothing new); other users' wallets are not reachable.

## Progress / evidence

Route: delegated writer (writer trigger: 2+ non-trivial files per task); no native review run by the writer.

- T1 4cf5da0: migration 4 `journal` (user_version 3 -> 4), `store.AddJournalEntries/Journal/JournalRefTypes`, `Backfill` persists every entry (also those without balance). RED: store tests failed to compile (undefined JournalEntry/AddJournalEntries), collector tests failed to compile (undefined NewEntries); then GREEN. Decision: the journal is only downloaded by `Backfill` (Run never fetches it), and each pass already fetches the full available journal, so inserting everything with INSERT OR IGNORE is the one-time backfill (a wallet with no rows is filled on its first pass, later passes add only new entries, second run inserts 0). Journal write failures are recorded per wallet and never touch balances. The old hardcoded `want 3` in users_test now uses `len(migrations)`.
- T2 b6bf952: `GET /api/wallets/{id}/journal` (limit 1-200 default 50, keyset cursor `<unix>-<id>`, ref_type, RFC 3339 from/to, 400 on bad params, 404 unknown or foreign wallet). RED: all new endpoint tests got 404 before the route existed; then GREEN.
- T3 6be506a: Movements view (button per wallet panel, table, type select, date range, Load more, aria-live status, focus management, Escape/Close), CSS classes, README. RED: ui_test `TestAppHasMovementsView` failed on missing strings; then GREEN.
- Checks at the end: gofmt -l . empty; go vet ./... clean; go test ./... ok; go test -count=5 store/collector/web ok; node --check app.js ok.
- Follow-up 566928d: Movements view pages with Previous / Next (client-side cursor stack, replaces rows, "Page N") instead of Load more; no API change. Route: delegated writer. RED: ui_test lacked Previous/Next/Page/cur.stack/movements-pager; then GREEN. Not verified: no browser run.
- Not verified: no browser run of the UI, no run against the real ESI or the real database.
- Follow-up 3bd9a83: Movements table in a fixed-height scroll region with sticky header, fixed column layout and truncated descriptions (full text in title), pager no longer moves. Route: delegated writer. RED: ui_test missing new strings; then GREEN.
