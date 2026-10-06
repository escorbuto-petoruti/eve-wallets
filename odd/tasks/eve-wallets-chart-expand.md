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
- [x] T2 Fix: expanded view no longer covers the time range section (non-modal panel in the flow right under it, Escape handler, refreshes on range change, closes if its panel has no points). Commit: `fix(web): keep the time range usable while a chart is expanded` (route: delegated writer). Evidence: gofmt -l clean, go build, go vet, go test -count=1 ./..., go test -race ./internal/web/..., node --check app.js all passed; not run in a browser, so placement, narrow-width layout and focus behavior are unobserved.
- [x] T3 Fix: the expanded view replaces the small charts while open (`chart-expanded` class on body hides the section grid and status card; a flex column fills the viewport, no guessed rem offset; the class is removed in the shared reset, so Close, Escape, no-points finish, section rebuild and opening elsewhere all clear it; small charts are still rebuilt while hidden and `resize()`d on show). Commit: `fix(web): let the expanded chart replace the small charts while open` (route: delegated writer). Evidence: gofmt -l, go build, go vet, go test -count=1 ./..., go test -race ./internal/web/..., node --check app.js all passed; not run in a browser, so fit-to-viewport, 480px layout and chart resize are unobserved.
- [x] T4 Feature: the expanded view has a Movements button (wallets only, hidden for the Total) in its header next to Close. Approach: keep `#sections` displayed while expanded and hide only the card children except the movements dialog (card chrome neutralised), so the modal movements dialog opens on top of the expanded view and returns focus to the expanded view's Movements button; no `.style` used. Commit: `feat(web): open a wallet's movements from the expanded chart` (route: delegated writer). Evidence: gofmt -l, go build, go vet, go test -count=1 ./..., go test -race ./internal/web/..., node --check app.js (results below in the commit report); not run in a browser, so the dialog stacking, focus return and empty-card layout are unobserved.

## Routing / test policy
- One delegated writer, one work-unit commit on `feat/chart-expand`; JS behavior covered by the literal-string UI test style plus `node --check` (no runnable JS RED).
- Delivery: forecast under 400 authored lines, single PR; push/PR stays the owner's decision (PR via the escorbuto-petoruti token).

## Progress
- Mapped: `buildPanel`/`drawSpark` in `internal/web/static/app.js` (~lines 840-930), dialog pattern in `buildMovements`.
