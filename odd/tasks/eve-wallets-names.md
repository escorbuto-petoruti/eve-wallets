# eve-wallets-names

## Objective
Show human names for wallets (corporation divisions above all) instead of "Division N": use the names ESI provides when the character can read them, and let the user rename any wallet by hand.

## Problem / why
In game, corporation wallet divisions carry custom names. ESI exposes them through `GET /corporations/{id}/divisions` (scope `esi-corporations.read_divisions.v1`, role Director, 1 h cache, only returns divisions whose name is not the default). The user's character is not a Director, so a manual rename must exist too.

## Decisions
- Precedence for the displayed name: user label > ESI name > default `Division N` (personal wallets: the character name as today). The in-game default division names were NOT verified, so the fallback stays `Division N`.
- Keep the user label and the ESI name in separate columns so an ESI refresh never overwrites a user label and the label can be cleared.
- Renaming is CLI-only (`eve-wallets wallets`, `eve-wallets label`). The HTTP API stays read-only: a write endpoint on an unauthenticated localhost server would be exposed to CSRF from any page open in the browser.
- ESI names are optional: no Director role or no scope is a recorded skip (`missing Director role` or `missing scope`), never an error. When the call succeeds, a division absent from the response has the default name, so its stored ESI name is cleared. When the call fails, stored ESI names are left untouched.
- Names are trimmed, 1-64 characters, no control characters.

## Constraints
- Same layout and rules as the eve-wallets MVP (see odd/tasks/eve-wallets.md). Money and tokens rules unchanged. Server text reaches the page only through `textContent`.
- Schema change is a new migration (user_version 2); existing databases migrate in place without data loss.

## Tasks
- [x] T1 store: migration 2 (`label`, `esi_name`), setters, effective name and source in the wallet queries + tests (route: delegated writer; RED observed as compile failure, GREEN: vet, `go test ./...`, `-count=10` store, gofmt clean)
- [x] T2 esi + collector: `CorporationDivisions`, collector integration (once per corporation, graceful skips) + tests (route: delegated writer; RED observed for the collector tests, GREEN: vet, `go test ./...`, `-count=10` esi+collector, gofmt clean; scope also edited the `fakeESI` in `cmd/eve-wallets/main_test.go` so it still satisfies `ESIClient`)
- [ ] T3 cmd + web + README: `wallets` and `label` commands, API fields `name` and `name_source`, page shows names, docs

## Routing / test policy
- Test-first with `go test ./...`; ESI faked. One delegated writer per task, one Conventional Commit per task on `feat/wallet-names`, stacked on `feat/eve-wallets-mvp` (HEAD fce3c36, unpushed).
- Native review boundary for assessments: fce3c36.

## Progress
Branch `feat/wallet-names` created from `feat/eve-wallets-mvp`. T1 done: migration 2, `SetLabel`/`ClearLabel`/`SetESIName`/`ClearESIName`, `ErrInvalidName`, `ErrNotFound`, `Wallet.DisplayName()`/`NameSource()`, tests in `internal/store/names_test.go`. T2 done: `esi.CorporationDivisions` (wallet names only, hangar parsed and ignored), `Report.NamesUpdated`, `ReasonMissingDirector`, names refreshed once per corporation in `Run` only (no scope: silent; 403: skip, a later character may succeed; failures leave stored names untouched), tests in `internal/collector/names_test.go` and `internal/esi/client_test.go`.

## Next step
T3.
