# eve-wallets-characters

## Objective
Phase 2 of the web login: a signed-in user registers more of their own EVE characters ("Add character"), and can move a character that already belongs to another user after confirming it.

## Problem / why
Phase 1 gives every character that signs in its own user (`UserID == CharacterID`). A person with several characters sees each one as a separate user and cannot see their wallets together.

## Decisions (owner)
- Adding a character that is already registered under ANOTHER user is refused by default (409-style message), but a move must exist.
- Move = option A: the add flow asks for confirmation ("this character belongs to another user; moving it removes it from their account"). The SSO round trip as that character is the proof of control; the previous owner does not take part.
- Only the character moves, never the whole account of its previous user. The previous user keeps its other characters and is deleted only when it is left with none (its sessions and wallet links cascade; wallets stay).
- The moved character's personal wallet is visible to the new user at once; corporation wallets follow after the next collection verifies the role, as today.
- Signing in with a character that is attached to a user signs in as that user (login no longer creates a user per character once the character belongs to someone).
- Scopes stay global (`sso.WalletScopes()`); no per-character scopes in this phase.
- No schema change: a new token row with `user_id` = the session user is enough (store stays v3).

## Constraints
- Artifacts in English; conventional commits; no AI attribution.
- Never steal a token silently: `SaveToken` upserts on `character_id`, so every add path checks the owner first.
- Same-origin and Host guards of `/auth/logout` apply to every new POST.
- A pending move lives server-side, single use, short TTL, bound to the session user and the browser (same model as `loginFlows`).
- ~400 authored changed lines per task is a planning heuristic only.

## Tasks
- [x] T1 store: `TokenOwner(characterID)`, `MoveToken(characterID, toUser)` that re-parents the token and deletes the previous user when it has no tokens left, `CharactersForUser(userID)`. Tests: owner lookup, move keeps other characters, empty user deleted with cascade, wallets remain. Evidence: `internal/store/users.go` (`UserCharacter`, `TokenOwner`, `CharactersForUser`, transactional `MoveToken` that wraps `ErrNotFound` for a missing token or target user, is a no-op for the same owner, moves only the personal wallet link and deletes the previous user when it has no token left); `internal/store/users_test.go` (`seedMoveFixture`, 310 lines added in total with the implementation). RED: package did not build (`s.TokenOwner undefined`, `undefined: UserCharacter`, `s.MoveToken undefined`), then GREEN: gofmt -l empty, go build, go vet, go test ./... ok, go test -race ./internal/store/... ok; parent spot check re-ran `go test ./internal/store` ok. Route: delegated writer (writer trigger: implementation plus tests). Commit: b9618d8.
- [x] T2 login by an attached character: callback in login mode signs into the owner user (`tokens.user_id`) and refreshes the token without touching `user_id`; unknown characters still create their own user. Tests next to `TestCallbackSuccess`. Evidence: `internal/web/auth.go` `callback` looks up `Store.TokenOwner`; an attached character gets no `users` row, its token keeps the owner's `user_id` and the session belongs to the owner; a lookup error answers 500 and never creates a user; tests `TestCallbackAsAnAttachedCharacterSignsInAsItsOwner`, `TestFailedTokenSaveForAnAttachedCharacterCreatesNoSession`, `TestFailedTokenOwnerLookupCreatesNoUserOrSession` (RED observed on all three, then GREEN); gofmt -l empty, go build, go vet, go test ./... ok, go test -race ./internal/web/... ok; parent spot check re-ran `go test ./internal/web` ok and read the `auth.go` diff (16 lines). Route: delegated writer. Commit: 4101501. Native review of the PR1 range main..a817020: assessed HIGH (hot_path, authentication in `internal/web/auth.go`, 485 changed lines), consent granted, 4 lenses, no blocker and no correction, approved and acknowledged (lineage review-f920e2fccb4d557f); reviewed boundary now a817020. 12 informational findings, listed under Follow-ups.
- [ ] T3 add-character flow: `loginFlow` gains intent and user; `GET /auth/add-character` (session required); callback attaches a new character, refreshes one already owned, and for another user's character holds a pending move; confirmation page and `POST /auth/move-character`. Tests: attach, re-add same user, collision refused without confirm, confirmed move, expired or foreign pending move, guards.
- [ ] T4 status and UI: `canSee` for character skips and errors uses the user's character set; `/api/me` returns the characters; page shows the list, an "Add character" button and the confirmation copy. Tests: status shows added characters' skips, ui structure test.
- [ ] T5 README and this document updated; not-verified list kept honest.

## Routing / test policy
- Test-first when a runnable deterministic test exists; RED observed before GREEN.
- Writers are delegated (2+ non-trivial files per task); one work-unit commit per task on `feat/add-characters`.
- Native review follows the receipt-driven switch per commit assessment.
- Delivery: forecast about 700 authored changed lines, above the 400 budget. Strategy chosen by the owner: `stacked-to-main`. Slices: PR1 = T1+T2 (store and login by an attached character), PR2 = T3 (add-character flow and move), PR3 = T4+T5 (status, UI, docs). Each PR bases on the previous slice branch; push and PR creation stay the owner's decision.

## Progress
- Design closed with the owner; mapping done (callback hardcodes `UserID = CharacterID` at `internal/web/auth.go:180`, `canSee` uses `u.CharacterID` at `internal/web/web.go:323`).
- Engram mirror: `odd/eve-wallets-characters/tasks`.

## Follow-ups
- PR1 review (informational, none blocking; only R1-001's claim was read in full): R1-001 `internal/web/auth.go:183-184` signing in as an attached character widens the session to the owner's other characters, latent until T3 because only `MoveToken` produces such a token; WARNINGs R2-001 `internal/store/users.go:197-206`, R2-003 `users.go:185-186`, R2-004 `internal/web/auth.go:184-189`, R3-001 `auth.go:178-185`, R3-002 `users.go:206`, R3-003 `users.go:167-168`, R4-001 `auth.go:177-185`, R4-002 `users.go:171-183`; SUGGESTIONs R2-002 `users.go:178`, R2-005 `users_test.go:440-447`, R2-006 this document lines 26-27. Read them before T3 touches the same code.
- A moved character leaves its previous user with stale corporation wallet links when that user has no other character in the corporation (the walker only adds links, never removes); only the personal wallet link is moved in T1.
- Remove-character action (option B, owner releases a character) is not part of this phase.
- Per-character scopes would need `AuthURL` to take scopes.

## Next step
T3 (add-character flow and move).
