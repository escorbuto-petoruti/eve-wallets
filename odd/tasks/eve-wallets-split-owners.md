# eve-wallets-split-owners

## Objective
Separate characters from corporations in the web UI. One section for characters and one section per corporation, each with its own wallet picker, balance chart, optional total and latest-balances table.

## Scope
Front end only (`internal/web/static/{index.html,app.js,style.css}`) plus UI tests in `internal/web/ui_test.go`. No API change: `/api/wallets` already returns `kind` and `owner_id`.

## Constraints
- No inline script/style/handlers (CSP `default-src 'self'`); server text only via `textContent`.
- Keep the 50-wallet cap per chart, the signed-out/collecting/empty flows and the status card.
- Decision (user): full separate sections, not tabs.

## Tasks
- [ ] T1 Section model: group wallets into a Characters section and one section per corporation, each with picker, chart, Total toggle, range buttons and latest table. Route: delegated writer (3 non-trivial files).
- [ ] T2 Tests in ui_test.go for the new markup/script contract; run `go test ./...`.

## Acceptance
- With 1 character and 2 corporations the page shows 3 sections, each charting only its own wallets.
- A section with no wallets is not rendered.
- `go test ./...` and `go vet ./...` pass.

## Progress / evidence
(none yet)
