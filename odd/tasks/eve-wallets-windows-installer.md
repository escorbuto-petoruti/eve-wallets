# eve-wallets Windows installer

## Objective
A Windows user installs eve-wallets with one PowerShell command, mirroring `install.sh`.

## Problem / why
Windows only has manual steps in the README (download zip, `Get-FileHash`, unzip, add to PATH). `install.sh` covers Linux and macOS; `eve-wallets update` refuses on Windows. Owner asked for an `install.ps1` on 2026-10-04.

## Decisions
- Scope: `install.ps1` (install, and update by re-running it), README docs. Out of scope: a Windows service, `eve-wallets update` support on Windows, signing, winget/scoop.
- Mirror `install.sh`: same release asset contract (`eve-wallets_<version>_windows_amd64.zip` + `checksums.txt`, SHA-256 verified before installing), same env overrides (`VERSION`, `INSTALL_DIR`, `EVE_WALLETS_RELEASE_BASE`), never needs admin rights, default dir under the user profile, no PATH change without telling the user.
- Windows arm64 has no release asset: the script must fail with a clear message on any architecture other than amd64.

## Constraints
- Windows PowerShell 5.1 and PowerShell 7 compatible; no external modules.
- The script must be testable against a local fake release like `install.sh` was, and any test must run on this Linux machine only if `pwsh` is available; otherwise say so honestly (never claim a Windows run that did not happen).

## Tasks
- [x] W1 `install.ps1` + `scripts/test-install-ps1.sh` (fake release over python http.server; happy path, reinstall/bak-prev, checksum mismatch, arm64, missing asset, iex survival) + CI step. Written and committed (1f657b2). Executed afterwards on Linux with a user-space PowerShell 7.6.6 tarball: 6/6 harness cases ok on the first run (no RED stage: the script was written before pwsh was available). Mutation check: turning the checksum `throw` into a `Write-Host` made `installed despite mismatch` FAIL, then restored, so the harness is not vacuous.
- [x] W2 (docs, no runner; structural readback) README: Install section for Windows (one-liner with `irm | iex` and the download-inspect-run variant), update-by-rerun note, remove the "Windows manual only" wording where it is now wrong, keep the not-verified list honest.

## Acceptance
`install.ps1` verifies the SHA-256, installs `eve-wallets.exe`, prints next steps and the PATH hint; failure modes exit non-zero with a clear message; README matches the code; existing Go checks stay green.

## Route
Delegated direct: one writer (2+ non-trivial files).

## Progress
Created 2026-10-04.
- W1 1f657b2: install.ps1, scripts/test-install-ps1.sh (skips without pwsh; REQUIRE_PWSH=1 in CI), ci.yml step (existing pinned actions; ubuntu-latest ships pwsh, assumed not verified). Only `sh -n` on the harness and a YAML parse were run; install.ps1 itself was never executed. Test-only env EVE_WALLETS_TEST_ARCH overrides arch detection.
- W2 (README commit, see git log): Windows section, wording fixed, honest not-verified bullet. Go checks: build, vet, gofmt, test all green.
- Not verified: everything about install.ps1 at runtime (pwsh 7, Windows PowerShell 5.1, real Windows, real GitHub redirect). Risk: CI step may fail on first run if the harness has a bug.
- `cmd/eve-wallets/update.go` Windows message could point to `install.ps1` (out of the writer's surface).

Still not verified: Windows PowerShell 5.1, a real Windows machine, the real GitHub redirect, and the CI step (ubuntu-latest shipping pwsh is assumed).

## Next step
Push and PR; confirm the CI step passes on its first run; ideally one run of `install.ps1` on a real Windows machine.
