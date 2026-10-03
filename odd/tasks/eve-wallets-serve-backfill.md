# eve-wallets-serve-backfill

## Objective
Make every collection cycle of `eve-wallets serve` also store the journal balances of the last 30 days, so the chart keeps per-transaction detail without running `backfill` by hand.

## Problem / why
`collect` and `serve` only take snapshots; the journal is loaded only by the manual `backfill` command. Real data confirmed it: the last journal point was 4 hours older than the last snapshot, so after the initial backfill the chart falls back to the snapshot resolution (30 min), and ESI only keeps 30 days of journal, so the detail is lost for good if `backfill` is not run in time.

## Decisions
- Each cycle: snapshot (`Run`) first, then journal backfill (`Backfill`). Backfill is idempotent (verified on the real SQLite file: a second run left the same rows), and ESI answers repeated journal requests from the ETag cache (journal cache is 1 h).
- If the snapshot step was rate limited, skip the backfill of that cycle. A backfill failure never stops the loop and never discards the snapshot already taken.
- One merged report per cycle feeds `/api/status`: skipped items and errors from both steps, rate-limit flag with the longest retry delay, and the number of journal points seen by the backfill.
- New `serve` flag `--no-backfill` to turn the journal step off (rate-limit safety valve). The standalone `backfill` command stays.
- Rate budget (from the ESI spec): 150 tokens per 15 min for character wallets and 300 for corporation wallets. A cycle costs roughly a dozen corporation journal calls plus a couple for the character, so one cycle per 30 minutes is well inside it.

## Constraints
- Same rules as the eve-wallets MVP (see odd/tasks/eve-wallets.md): no tokens in output, API stays GET/HEAD only, loopback only.
- `collect` is unchanged (snapshot only).

## Tasks
- [x] T1 cycle composition in `cmd/eve-wallets` (+ merged status in `internal/web`) + `--no-backfill` + tests + README. Evidence: `newCycle` in `cmd/eve-wallets/cycle.go`, `Report.JournalPoints`, `/api/status` `journal_points`; cycle tests RED (undefined newCycle) then GREEN; go vet clean, go test ./... ok, -count=10 ok, gofmt clean. Route: delegated writer.

## Routing / test policy
- Test-first with `go test ./...`; ESI and eve-auth faked. One delegated writer, one Conventional Commit on `feat/serve-backfill`.
- Native review boundary for assessments: main (1ab196a).

## Progress
T1 implemented and verified on `feat/serve-backfill`; one work-unit commit. Real-ESI run of the new cycle not done (fakes only).

## Next step
Open a PR (delivery is the user's call).
