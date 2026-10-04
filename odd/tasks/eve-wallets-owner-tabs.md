# eve-wallets-owner-tabs

## Objective
Show one owner at a time: tabs "Characters" and one per corporation, only the active tab's picker, chart, total and latest table are visible.

## Context
Replaces the stacked sections of eve-wallets-split-owners (user: "no uno a continuación del otro").

## Scope
internal/web/static/{index.html,app.js,style.css} and internal/web/ui_test.go. No API change.

## Constraints
- Shared time-range control stays on top and applies to the active tab.
- Only the active tab keeps a live chart; switching tabs destroys the previous chart and draws the new one.
- The selected tab survives the periodic reload (polling) and is reset if its owner disappears; default is the first tab.
- Accessible tabs: role=tablist/tab/tabpanel, aria-selected, aria-controls, roving tabindex, arrow keys / Home / End.
- No inline script/style/handlers; server text via textContent only; 50-wallet cap per chart kept.
- A single owner still renders (one tab or no tab bar, writer decides) without extra clicks.

## Tasks
- [x] T1 Tab bar built from characters + corporations; activating a tab shows only its panel.
- [x] T2 Chart lifecycle per active tab (destroy on switch, token invalidation), selection persistence across reloads.
- [x] T3 Keyboard/ARIA, styles, UI tests; go vet and go test pass.

## Acceptance
- With 1 character and 2 corporations: 3 tabs, one panel visible, switching changes the chart and table.

## Progress
- T1-T3 implemented; verified by go vet, go test, node --check and static UI contract tests (RED observed against the old app.js). Not exercised in a real browser.
