# eve-wallets easier start on Windows

## Objective
A Windows user can type `eve-wallets serve` from any terminal and the sign-in page opens by itself.

## Problem / why
After `install.ps1` the user must type the full path of the exe (the installer only prints a PATH hint) and copy `http://localhost:8088` into a browser. Verified on a real Windows machine on 2026-10-04 (v0.1.1); the owner asked for both simplifications.

## Decisions
- PATH (installer): opt-in. When the host is interactive, ask `Add <dir> to your user PATH? [y/N]` (default No); `EVE_WALLETS_ADD_TO_PATH=1` adds without asking and `=0` never asks nor adds (for non-interactive runs and `irm | iex` automation). Only the user-scope PATH is touched, never the machine PATH; idempotent (no duplicate entry, case-insensitive compare, trailing slash tolerant); tell the user to open a new terminal. The installer must still never need admin rights.
- Browser (binary): `serve` opens `http://localhost:<port>` after the listener is up. Default on only on Windows and only when stdout is a terminal (a service or scheduled task never opens a browser; Linux/macOS/WSL unchanged). `--open` forces it on any OS, `--no-open` disables it. It opens only for a loopback address (the API refuses non-loopback anyway). A failure to open the browser is a one-line notice, never an error, and the server keeps running. The opener is injectable so tests never launch a real browser.
- Out of scope: autostart/Task Scheduler, Start Menu shortcut, winget/scoop, machine-wide PATH.

## Constraints
- Windows PowerShell 5.1 and PowerShell 7 compatible; Linux/macOS behavior of `serve` unchanged by default.
- The PATH logic must be testable on Linux (injectable getter/setter or a test-only env hook, documented like `EVE_WALLETS_TEST_ARCH`); never claim a real-registry run that did not happen.

## Tasks
- [x] E1 `install.ps1`: opt-in user PATH entry (prompt or `EVE_WALLETS_ADD_TO_PATH`), idempotent, next-steps text updated, harness cases (add, already present, declined/never, non-interactive default no change) + README.
- [x] E2 `serve` opens the browser (Windows+TTY default, `--open`/`--no-open`, loopback only, injectable opener, failure is a notice) + tests + usage text + README.

## Acceptance
`go build`, `go vet`, `gofmt -l .`, `go test ./...`, `GOOS=windows go build ./...`, and the install.ps1 harness (pwsh 7) pass; each behavior change has a test seen RED then GREEN; README matches the code; what was not run on real Windows is stated.

## Route
Delegated direct: one writer (2+ non-trivial files across Go, PowerShell and the harness).

## Progress
- E1 done, commit be4e765 (delegated writer). RED: 7 new harness cases failed before the change (PATH untouched, no outcome text); GREEN: all 13 harness cases pass under pwsh 7 (8 old + 5 new: default non-interactive, hint text, =0, =1 adds, rerun no duplicate with trailing slash). Choices: entry appended to the user PATH; session `$env:Path` is not modified (the user is told to open a new terminal); non-interactive when the host is -NonInteractive, UserInteractive is false, or stdin/stdout is redirected; a Read-Host failure counts as No.
- E2 done, commit 850e6e8. RED: browser_test.go failed to compile (shouldOpenBrowser undefined) before the change; GREEN: table tests (windows+tty on, windows no tty off, linux off, --open on linux, --no-open on windows off, non-loopback never, ipv6/localhost) plus serve tests with a fake opener (URL uses the bound port, opener error is a stderr notice and the server keeps running, --open with --no-open exits 2). The real opener is `rundll32 url.dll,FileProtocolHandler <url>` (windows), `open` (darwin), `xdg-open` (other), URL as its own argv element; stdout terminal detection is a ModeCharDevice check.
- Verified (Linux): go build, go vet, gofmt -l, go test ./..., GOOS=windows go build ./... and go vet ./cmd/..., full install.ps1 harness, sh -n.
- NOT verified: real Windows, Windows PowerShell 5.1, the real user-scope registry PATH write and the [y/N] prompt in a real console, a real browser launch with rundll32 (PATH logic used EVE_WALLETS_TEST_PATH_FILE; the opener was faked).
- E3 fix (review findings R3-user-path-writeback, R4-user-path-expansion): `Get/Set-EveUserPath` now read and write HKCU\Environment directly (`DoNotExpandEnvironmentNames`, existing value kind kept, ExpandString when absent), so other entries and `%VAR%` references are left byte-for-byte; after the write a throwaway user variable is set and removed to broadcast WM_SETTINGCHANGE (failure only prints a notice); registry errors throw and leave PATH untouched; non-Windows returns an empty PATH. The "already present" check also compares each entry with `%VAR%` expanded. Evidence: the new harness case for an entry `%LOCALAPPDATA%/bin` equal to the install dir failed before (duplicate added), passes now; the case keeping `%USERPROFILE%\bin;C:\tools` passed already (the test hook never expanded, so no meaningful RED). Harness 15 cases pass under pwsh 7 on Linux. NOT verified: the registry read/write itself, the WM_SETTINGCHANGE broadcast, Windows PowerShell 5.1 (reviewed by reading only).

## Next step
Review, then push and open a PR (human decision).
