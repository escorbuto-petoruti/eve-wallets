# eve-wallets double-click start on Windows

## Objective
A Windows user double-clicks something, eve-wallets starts and opens the page, and the user has an obvious way to stop it. The browser stays the UI; no tray, no autostart, no own window.

## Problem / why
Double-clicking `eve-wallets.exe` today prints the usage and exits with code 2 (`run` in `cmd/eve-wallets/main.go`, `len(args) == 0`): the console flashes and closes. The installer creates no shortcut. There is no way to stop the server except closing its console or Ctrl+C. The owner chose, on 2026-10-04, a browser-based experience without a background process: history is recovered by the backfill that every `serve` start runs (ESI keeps 30 days of journal, see the README).

## Decisions
- D1 No arguments on Windows run `serve` with default flags (the existing default opens the browser when stdout is a terminal, which a double-click console is). On Linux/macOS no arguments still print the usage and exit 2. The OS is injectable (`deps.goos`) so it is testable. `serve` prints one clear line in the console saying where it listens and how to stop it (close this window, press Ctrl+C, or use the Quit button in the page).
- D2 A Quit button in the page header for a signed-in user, with a confirmation step, calling `POST /api/shutdown`. The endpoint requires a session (same `requireUser` as the rest of the API) AND the same cross-site guard as sign-out (Sec-Fetch-Site first, then Origin/Referer, plus the loopback Host check), so another website cannot stop the server. It answers 202 and the process shuts down gracefully through the same path as Ctrl+C; the page then says it stopped and the tab can be closed. Signed-out visitors get no Quit button and no unauthenticated endpoint. The shutdown trigger is an injected callback so tests never stop a real process. UI strings are in English like the rest of the page.
- D3 `install.ps1` can create shortcuts, opt-in like the PATH: in an interactive host it asks `Create shortcuts for eve-wallets on your Desktop and Start menu? [y/N]` (default No); `EVE_WALLETS_ADD_SHORTCUT=1` creates them without asking and `=0` never asks nor creates. Both are `.lnk` files named `eve-wallets` in the user's Desktop and Start Menu Programs folders (resolved with `[Environment]::GetFolderPath`, so redirected folders work), target the installed `eve-wallets.exe` with no arguments, working directory the install dir; created through the `WScript.Shell` COM object (no admin, PS 5.1 + 7); running the installer again replaces them (idempotent). Linux cannot create `.lnk`, so a documented test-only hook (`EVE_WALLETS_TEST_SHORTCUT_DIR`) makes the installer record the intended shortcut target/dir in a text file instead; never claim a real `.lnk` was created on Linux.
- Out of scope: tray icon, autostart, own window/webview, installer package (MSI), uninstall, code signing, Defender handling.

## Constraints
- Linux/macOS behavior unchanged by default; Windows PowerShell 5.1 and 7 compatible; no admin rights.
- The page has no framework (vanilla JS in `internal/web/static/app.js`): follow its style and the existing 401/session handling.

## Tasks
- [x] F1 (D1) no-args on Windows runs serve + console banner + tests (table over goos) + usage/README.
- [x] F2 (D2) `POST /api/shutdown` (session + same-origin guard + injected stop callback) + Quit button with confirmation and stopped state in the page + tests (auth required, cross-site refused, callback called once, page markup) + README security note.
- [x] F3 (D3) `install.ps1` opt-in shortcuts + harness cases through the test hook + README.

## Acceptance
`go build`, `go vet`, `gofmt -l .`, `go test ./...`, `GOOS=windows go build ./...`, `GOOS=windows go vet ./cmd/...`, `node --check internal/web/static/app.js` if node exists, and the install.ps1 harness under pwsh 7 pass; each behavior change has a test seen RED then GREEN; README matches the code; everything not run on real Windows (double-click console, the Quit flow in a real browser, real `.lnk` creation, PS 5.1) is stated as unverified.

## Route
Delegated direct: one writer (3 tasks across Go, web UI, PowerShell and the harness).

## Progress
Created 2026-10-04. Route: delegated direct (one writer), all three tasks.
- F1 done, commit 75553ab. RED: TestNoArgumentsDependOnTheOS failed (windows printed the usage); GREEN after `run` maps no args to `serve` on windows and `serve` prints "Close this window, press Ctrl+C or use the Quit button in the page to stop." Usage and README updated.
- F2 done, commit cc77da0. RED: web tests did not compile (no Deps.Shutdown), cmd test got 404, markup tests failed; GREEN with `POST /api/shutdown` (requireUser + sameOrigin + loopback Host, 202, sync.Once), `runServe` wiring through a context cancel (same path as Ctrl+C, exit 0), Quit button with inline confirmation and stopped state. README security note.
- F3 done, commit recorded below. RED: 4 new harness cases failed; GREEN after `New-EveShortcuts` (opt-in prompt, EVE_WALLETS_ADD_SHORTCUT, EVE_WALLETS_TEST_SHORTCUT_DIR hook). README Windows section updated.
- Not verified: real Windows (double-click console, PS 5.1), real `.lnk` creation via WScript.Shell and redirected folders, the Quit flow in a real browser (only markup/JS structure tests and `node --check`), the [y/N] shortcut prompt in a real console.
- Known pre-existing flake: TestServeCollectsAndShutsDownCleanly sometimes needs more than its 5s to stop under repeated or -race runs (reproduced on the F1 commit); not changed here.

## Next step
Parent review, then push/PR decisions by the owner.
