# eve-wallets post-login hardening

## Objective
Fix the five worthwhile informational findings left open by the web-login reviews, in one small PR.

## Problem
The login/status/sign-out reviews (T1, T3, T4, T9, T8) closed with non-blocking findings. A read-only triage on 2026-10-04 judged five of them real and cheap.

## Scope (authorized by the owner on 2026-10-04)
- Branch: `fix/post-login-hardening`. Delivery: one PR, human-owned push/merge.
- Out of scope: the other findings (client-id literal, login-flow eviction, scheduler trigger drop, cookie scope, T5/T6 readability, unclassified T3/T4 items).

## Tasks
- [x] H1 `internal/sso/jwks.go` `key()`: stop holding the mutex across the network fetch and add a minimum interval between refetches for unknown `kid`s (no refetch amplifier). Test: RED first (concurrent/unknown-kid refetch count).
- [x] H2 `internal/collector/collector.go` `ItemError.Error()`: nil-guard `Err`. Test: RED first (nil Err no longer panics).
- [x] H3 `R3-CORP-SKIP-HIDDEN` `internal/web/web.go` `status`: a corporation skip for a corporation not yet linked to the session user must still reach its owner when the skip's owner identity says so. Investigate the data first; if `Skip` lacks the identity needed, stop and report instead of guessing. Never show another user's skip.
- [x] H4 `internal/auth/tokens.go` (~153): treat a `GetToken` read error in the post-refresh re-check as a failure instead of swallowing it and overwriting possibly fresh credentials. Test: RED first.
- [x] H5 `internal/web/auth.go` logout: do not ignore the `DeleteSession` error (the user must not be told they signed out while the server session survives); add a `Sec-Fetch-Site` logout test (`R3-SECFETCH-TEST-GAP`).

## Acceptance
`go build ./...`, `go vet ./...`, `gofmt -l .` empty, `go test ./...` and `go test -race` on touched packages pass; each behavior change has a test seen RED then GREEN.

## Route
Delegated direct: one writer (writer trigger: 5 changes over 5+ non-trivial files).

## Progress
- H1 done, commit 128f427. RED: unknown-kid refetch count 11 (want 1) and key() blocked behind an in-flight fetch with a cancelled context. GREEN: fetch outside the mutex with a shared in-flight fetch, minimum 1 minute between refetches once keys are loaded (injectable clock); existing rotation test advances the clock.
- H2 done, commit 432e8e4. RED: nil-pointer panic in ItemError.Error. GREEN after the nil guard.
- H3 done, commit 4bbf04d. Skip already carries UserID (the user whose character produced it), so a corporation skip is now shown on that ownership alone (u.UserID != 0), no longer requiring the corporation to be linked. Other users' skips and identity-less skips stay hidden. RED: unlinked-corp skip absent. TestStatusScopesSkipsAndErrorsToUser updated: Alice now also sees her own unlinked-corporation skip.
- H4 done, commit 6c63fdc. RED: no error when the re-check read fails. GREEN: returns an error and saves nothing (the rotated token is not kept in memory, per the task).
- H5 done, commit 8ded76a. RED: logout answered 303 despite a failing DeleteSession (SQL trigger). GREEN: logs, clears the cookie and answers a 500 error page. The Sec-Fetch-Site logout tests already exist (TestLogoutFetchMetadata), so no new test was needed for them.

Verification: go build, go vet, gofmt -l, go test ./... and go test -race on touched packages (see final report).

## Next step
Human review, push and PR.
