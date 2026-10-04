# eve-wallets-web-rename

## Objective
Let a signed-in user rename corporation wallets from the web UI when ESI does not provide the division name. Division 1 (always "Master Wallet") is excluded.

## Problem / why
Renaming exists only through `eve-wallets label` (CLI). The HTTP API was read-only because the server had no login; it now has sessions and Origin checks, so a write endpoint is viable.

## Decisions
- Rename is offered only for corporation wallets with `name_source != "esi"` and `division != 1`. The server enforces the same rule (UI is not the guard).
- The label is stored per wallet (existing `label` column), so it is shared by every user who can see that wallet. A user may only rename wallets they have access to.
- Empty name clears the label (back to ESI/default). Name rules unchanged: trimmed, 1-64 chars, no control characters (`store.ErrInvalidName`).
- Write endpoint reuses the sign-out CSRF protections (same-origin check) and requires a valid session. CSP unchanged, no inline script/style, server text only via `textContent`.

## Tasks
- [x] T1 backend: authorized, CSRF-checked POST endpoint to set/clear a label with the rules above + tests (RED first).
- [x] T2 frontend: rename control in the corporation wallet panel (inline edit: save, cancel, reset), hidden for Division 1 / ESI-named / personal wallets + tests + README.

## Acceptance
- Corp wallet without ESI name can be renamed and reset from the page; Division 1 and ESI-named wallets cannot (UI and API); cross-user and cross-origin requests are rejected.

## Progress / evidence
- Route: delegated writer (T1+T2, one writer; 2+ non-trivial files). T1 RED observed (all rename tests 405 before the route existed), then GREEN. T2 covered by literal-string UI tests and `node --check` only; no browser run. Not committed.
