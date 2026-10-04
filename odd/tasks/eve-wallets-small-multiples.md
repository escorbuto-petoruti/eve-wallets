# eve-wallets-small-multiples

## Objective
Show how each wallet evolves: replace the single shared-axis line chart with one panel per wallet (small multiples), each with its own Y scale.

## Scope
- `internal/web/static/app.js`, `internal/web/static/style.css` (and `index.html` only if needed).
- No backend or endpoint changes; data comes from the existing `resp.series` / `resp.total`.
- Base: stacked on `feat/dark-restyle` (PR #17).

## Constraints
- CSP `default-src 'self'` (no external scripts, fonts or inline styles/scripts); Chart.js is vendored.
- Keep owner tabs, range buttons and the Total.
- Fixed color per wallet (never repainted by filters). Thin marks, recessive grid, no legend per panel (title names it).

## Tasks
- [x] T1 Panel per wallet: title (portrait/logo), current balance, delta over range (ISK and %), stepped sparkline, crosshair tooltip. Total becomes one more panel. Drop selection checkboxes (cap the panel count).
- [x] T2 Tests: update/extend `internal/web` UI tests; `go test ./...` green.

## Acceptance
- Each wallet readable on its own scale; all visible at once; tabs and range buttons still work.

## Progress / evidence
- Route T1: delegated writer (2 non-trivial files: app.js + style.css).
- T1/T2 done (uncommitted): panels in app.js/style.css; ui_test.go updated + TestAppBuildsSmallMultiplePanels; gofmt/vet/test green.

- Commit: e371429 (feat(web): show one chart panel per wallet). Spot check: go test ./... green. Not run in a browser; no RED test was written first (the new test was added after the implementation).
