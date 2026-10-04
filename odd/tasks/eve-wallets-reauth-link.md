# eve-wallets-reauth-link

## Objective
When EVE SSO rejects a stored refresh token (invalid_grant), show a highlighted notice in the page naming the character, with a direct link to /auth/add-character to sign in again.

## Scope
- internal/web/status.go and the /api/status handler: expose a `reauth` list ([{character_id, name}]) derived from collector errors where errors.Is(err, auth.ErrReauthRequired). Scoped to the signed-in user's own characters, like the existing status scoping.
- Front end (internal/web/static): render the notice with the link, replace the plain error line for those characters.
- Tests for the status JSON and the UI contract.

## Constraints
- No tokens in any output; server text via textContent only; CSP unchanged.
- The SSO login cannot preselect a character: copy must tell the user to choose that character on the EVE screen.
- Re-adding the same character refreshes its token (finishAdd / SaveTokenIfOwner), no new backend flow.

## Tasks
- [x] T1 Status API exposes per-user `reauth` list (RED test first).
- [x] T2 UI notice with direct link + UI tests; go vet and go test pass.

## Acceptance
- A character whose refresh fails with invalid_grant appears in `reauth` for its owner only, and in the page as a warning with an "Sign in again" link to /auth/add-character.
- Other errors keep the current rendering.

## Progress
- T1/T2: RED observed (3 new tests failing), then GREEN; go vet, go test ./..., node --check pass. Uncommitted by instruction.
