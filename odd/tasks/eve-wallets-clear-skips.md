# eve-wallets-clear-skips

## Objective
Make the "Last collection" skip messages for corporations accurate and non-redundant.

## Problem
- "Corp <id>: already collected" is logged for every extra character of an already collected corporation; it is informational, not a problem.
- The same corporation shows up under its real name and under the fallback "Corp <id>" (the already-collected branch uses the fallback), so one issue reads as two.
- "missing Director role" does not say what is lost.

## Scope
internal/collector/collector.go (walker.corp, corporationNames, skip), its tests, and any status/UI text that depends on the old strings.

## Tasks
- [x] T1 Remember the real corporation name per pass and use it in every skip/error of that corporation (fallback only when the name lookup failed).
- [x] T2 Stop recording ReasonAlreadyCollected as a skip.
- [x] T3 Deduplicate skips per (owner kind, owner id, reason) within a pass.
- [x] T4 Reword the Director reason to state the consequence (division names cannot be read; default names are shown). Update tests; go vet and go test pass.

## Acceptance
- One corporation yields one skip line per distinct reason, under its real name.
- No "already collected" line.

## Progress / evidence
Parent verification: go vet ./... clean; go test -count=1 ./... all packages ok; diff reviewed against T1-T4.
