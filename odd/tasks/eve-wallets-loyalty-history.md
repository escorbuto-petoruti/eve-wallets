# eve-wallets-loyalty-history

## Objective
Keep the history of each character's loyalty points per corporation, so their evolution can be shown later.

## Problem / why
`loyalty_points` only holds the latest snapshot per (character, corporation); every collection replaces it, so past values are lost. ESI offers no LP history, so it must be recorded from now on.

## Decisions
- Append-only table `loyalty_history` (character_id, corporation_id, taken_at, points). A row is appended only when the points differ from the last stored value for that pair (or there is none), so a 30-minute cycle does not store identical rows.
- When ESI stops returning a corporation that had points, a single `0` row is appended (the balance went to zero) and no more rows follow until it changes again.
- The migration seeds the history with the current `loyalty_points` rows (taken_at = their fetched_at) so existing data is not lost.
- History rows follow the token like the snapshot: they are removed when the character's token is deleted and survive a token move.
- Scope: storage plus a read-only API for a user's own characters. No UI/chart in this task.
- Same-transaction write with the snapshot replacement; LP failures never affect ISK balances, journal or renames.

## Constraints
- Integers only; no new dependencies; existing databases migrate in place (read the current user_version from the code); API GET-only with the existing guard/auth/headers; per-user scoping (other users' characters are never exposed).

## Tasks
- [x] T1 store + collector: migration (table, index, seed), append-on-change inside `ReplaceLoyalty`, zero row for vanished corporations + tests (RED first)
- [x] T2 API: `GET /api/loyalty/history` (own characters, filters, bounded) + README + tests (RED first)

## Acceptance
- Two collections with the same points store one row; a change stores a new row; a corporation that disappears stores a single zero; the migration seeds existing rows; history is only reachable for the signed-in user's characters.

## Progress / evidence
- Route: delegated writer (one bounded writer for T1 and T2; parent explored nothing beyond the spec).
- T1 `88efc89`: RED observed (`no such table: loyalty_history`, 5 new store tests failing), then GREEN. Migration 6 (`loyalty_history`, PK (character_id, corporation_id, taken_at) doubling as the index, seed via INSERT OR IGNORE from `loyalty_points`), append-on-change and single zero row inside `ReplaceLoyalty`. The collector needed no change (it calls `ReplaceLoyalty`; fakeStore untouched).
- T2 `5ead264`: RED observed (404 for every new route), then GREEN. `GET /api/loyalty/history` with keyset cursor `<taken_at>-<corporation_id>`, limit default 500 max 2000, 400 on bad params, 404 for unknown/foreign character; README updated.
- Checks (after T2): gofmt -l . clean; go vet ./... clean; go test -count=1 ./... all ok; go test -count=5 store/collector/web ok.
- Not verified: the real database migration, a real ESI call, the running service.
