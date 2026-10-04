# eve-wallets-distribution

## Objective
Let a small circle of people (friends, corp mates) install and update eve-wallets easily. The repository will be made public (by the owner, as the last step), under the MIT license, with GitHub releases carrying the binaries. Later it may open up to anyone.

## Problem / why
Today installing is `go build` plus copying the binary, there is no license, no CI, no releases, no version command, and updating is manual and delicate: the schema migrates automatically on open and an older binary refuses a newer database, so a backup must be taken by hand before updating.

## Decisions
- Audience: option 2 (a close circle), designed so it can grow into public use. Repo public, license MIT (copyright holder `escorbuto-petoruti`, year 2026). The git history stays as it is (the author email is accepted as public).
- Release assets contract (installer and updater depend on it): one archive per platform named `eve-wallets_<version>_<os>_<arch>.tar.gz` (`.zip` for windows), containing the binary `eve-wallets` (`eve-wallets.exe` on windows), `LICENSE` and `README.md`, plus a `checksums.txt` with SHA-256 lines (`<hash>  <archive name>`). `<version>` is the tag without the leading `v`. Platforms: linux and darwin on amd64 and arm64, windows on amd64.
- Linux and macOS get an `install.sh` and an `eve-wallets update` command; windows users download the zip by hand (documented), no self-update there.
- Safety for updates: before applying a schema migration the app copies the database to `wallets.db.bak-v<previous user_version>` (a consistent copy, file mode 0600) and never overwrites an existing backup of that version.
- The embedded EVE client id and the callback `http://localhost:8088/auth/callback` stay; document that `--addr` must keep port 8088 for sign-in to work with the shared application, and how someone can use their own application.
- README carries the CCP disclaimer (not affiliated with CCP Games; EVE Online and related marks are trademarks of CCP hf.).
- Publishing is out of scope for the writer: no tags, no releases, no visibility change, no pushes. The owner does those steps explicitly.

## Constraints
- No new Go dependencies unless unavoidable (prefer the standard library). Cross-compilation must work with CGO disabled. Workflow actions pinned to full versions. No secrets needed by CI beyond the default `GITHUB_TOKEN`.
- Tests (RED first) for Go behaviour; shell and workflow files are validated with the tools available locally (syntax checks, cross-compiles, a local dry run of the installer against a locally served fake release).

## Tasks
- [x] T1 LICENSE (MIT) + README: CCP disclaimer, Install, Update, Run as a service (systemd, Linux/macOS notes, Windows manual), client id and port 8088 note
- [x] T2 `eve-wallets version` (and `--version`), default `dev`; automatic database backup before a schema migration + tests
- [x] T3 CI (`.github/workflows/ci.yml`: gofmt, vet, test on push and PR) and release workflow (`release.yml` on tags `v*`: test, cross-build the platforms above with `-ldflags "-X main.version=<version>"`, archives, `checksums.txt`, GitHub release with generated notes)
- [x] T4 `install.sh` (detect os/arch, download the release archive, verify SHA-256, install to `~/.local/bin` or a chosen dir, optional systemd user unit, print next steps) and `eve-wallets update` (check latest release, compare versions, verify checksum, replace the binary atomically, back up first, never during a migration) + tests

## Acceptance
- From a tagged release a friend runs one command to install and one to update on Linux/macOS; windows has documented manual steps; a migration never runs without a fresh backup; `version` reports the release.

## Progress / evidence

Route: delegated writer (one bounded writer, tasks in order, one commit each).

Commits (branch feat/distribution):
- T1 068fab7 docs: LICENSE (MIT), README (disclaimer, Install, Update, systemd, sign-in/port 8088) and the `EVE_WALLETS_CLIENT_ID` override (there was no override before; `ssoConfig` in cmd/eve-wallets/main.go, RED: undefined ssoConfig, GREEN with tests).
- T2 9c25615 `version`/`--version` and `internal/store/backup.go` (VACUUM INTO a private temp file, renamed to `<db>.bak-v<N>`, 0600, never overwritten, open fails before migrating if the backup cannot be written). RED: undefined BackupPath and unknown command "version". No startup log line for the backup was added (no obvious place; `Store.BackupPath()` exposes it).
- T3 74c2d22 `.github/workflows/ci.yml`, `release.yml`, `scripts/package.sh`. Actions pinned to major tags (`actions/checkout@v4`, `actions/setup-go@v5`) because exact release versions could not be determined offline; pin to full versions/SHAs before going public.
- T4 704a6f5 `install.sh` and `eve-wallets update` (`internal/update`, `cmd/eve-wallets/update.go`). RED: undefined update.Options / unknown command "update".
- Task file update: this commit.

Observed:
- gofmt -l . empty; go vet ./... clean; go test -count=1 ./... all packages ok; go test -count=5 ./internal/store/... ./internal/update/... ok.
- Cross-compile with CGO_ENABLED=0 (go build -trimpath ./cmd/eve-wallets): linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64 all succeed, no code changes needed.
- scripts/package.sh 9.9.9: 5 archives + checksums.txt; `sha256sum -c checksums.txt` all OK; archive contents binary + LICENSE + README.md; extracted linux binary prints `eve-wallets 9.9.9` for `version` and `--version`. No `zip` binary on this machine, so the windows zip was produced by the python3 zipfile fallback (the release runner uses `zip`; untested path).
- YAML: both workflows parse with python3 yaml (PyYAML 6.0.1); structure checked (triggers, jobs, permissions), not executed.
- install.sh against a local python http.server serving a fake release (latest redirect, pinned VERSION, bak-prev kept, checksum mismatch aborts with rc=1 leaving the old binary and no temp files, missing release rc=1, bad arg rc=1, `--systemd` with a fake systemctl and temp XDG_CONFIG_HOME writes the unit and calls daemon-reload and enable --now). `sh -n` and dash OK; shellcheck is not installed (not run).
- `eve-wallets update --check` and `update` end to end against the same local server: 0.0.1 -> 9.9.9 applied, `.bak-prev` kept.

Not verified:
- A real GitHub Actions run (ci.yml, release.yml), a real release, `gh release create` flags, and install.sh from the real raw.githubusercontent.com URL; `update` against the real GitHub API.
- Behaviour on macOS and Windows (only cross-compiled), the real systemd service (the running one was not touched), and the real database migration.
- shellcheck; the zip tool path of package.sh.
- README mention that the repository has not been published: visibility, tags, pushes are left to the owner.
