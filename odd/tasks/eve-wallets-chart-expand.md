# eve-wallets-chart-expand

## Objective
Let the owner open an enlarged version of each wallet balance chart (and the Total chart) in a dialog.

## Problem / why
The per-wallet charts are small sparklines (ticks limited to 4 per axis, no legend), so trends and values are hard to read.

## Decisions
- An "Expand" button on every panel (wallets and Total) opens a dialog with the same data, drawn large, with full axes and tooltips.
- Pure front-end: reuses the data already loaded for the panel; no new endpoint.
- Same modal pattern as the Movements dialog (native `<dialog>`, Escape or Close returns focus to the button, charts destroyed on close, theme colors read at open).

## Constraints
- Artifacts in English; conventional commits; no AI attribution lines.
- ~400 authored changed lines per task is a planning heuristic only.

## Tasks
- [x] T1 UI: Expand button per panel, enlarged chart dialog, CSS, structural UI test, README note. Commit: `feat(web): expand each balance chart in a dialog` on feat/chart-expand (id in git log; not self-referenced) (route: delegated writer). Evidence: gofmt clean, go build/vet, go test -count=1 ./..., go test -race ./internal/web/..., node --check app.js all passed; not run in a browser.

## Routing / test policy
- One delegated writer, one work-unit commit on `feat/chart-expand`; JS behavior covered by the literal-string UI test style plus `node --check` (no runnable JS RED).
- Delivery: forecast under 400 authored lines, single PR; push/PR stays the owner's decision (PR via the escorbuto-petoruti token).

## Progress
- Mapped: `buildPanel`/`drawSpark` in `internal/web/static/app.js` (~lines 840-930), dialog pattern in `buildMovements`.
