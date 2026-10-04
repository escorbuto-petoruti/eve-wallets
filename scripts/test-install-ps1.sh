#!/bin/sh
# Run install.ps1 against a locally served fake release (needs pwsh and python3).
#
#   scripts/test-install-ps1.sh
#
# Without pwsh it skips (exit 0) unless REQUIRE_PWSH=1, which CI sets.
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
if ! command -v pwsh >/dev/null 2>&1; then
	if [ "${REQUIRE_PWSH:-0}" = 1 ]; then
		echo "pwsh is required" >&2
		exit 1
	fi
	echo "SKIP: pwsh not found, install.ps1 was not run"
	exit 0
fi
command -v python3 >/dev/null 2>&1 || {
	echo "python3 is required" >&2
	exit 1
}

WORK=$(mktemp -d)
SERVER_PID=
cleanup() {
	[ -z "$SERVER_PID" ] || kill "$SERVER_PID" 2>/dev/null || true
	rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

V=9.9.9
REL="$WORK/rel/download/v$V"
mkdir -p "$REL"
python3 - "$REL" "$V" <<'PY'
import hashlib, sys, zipfile
rel, v = sys.argv[1], sys.argv[2]
name = f"eve-wallets_{v}_windows_amd64.zip"
with zipfile.ZipFile(f"{rel}/{name}", "w") as z:
    z.writestr("eve-wallets.exe", "fake exe\n")
    z.writestr("LICENSE", "mit\n")
h = hashlib.sha256(open(f"{rel}/{name}", "rb").read()).hexdigest()
open(f"{rel}/checksums.txt", "w").write(f"{h}  {name}\n")
PY
# A second release whose checksum does not match, and one with no zip at all.
BAD="$WORK/rel/download/v8.8.8"
mkdir -p "$BAD" "$WORK/rel/download/v7.7.7"
cp "$REL/eve-wallets_${V}_windows_amd64.zip" "$BAD/eve-wallets_8.8.8_windows_amd64.zip"
printf '%s  eve-wallets_8.8.8_windows_amd64.zip\n' \
	0000000000000000000000000000000000000000000000000000000000000000 >"$BAD/checksums.txt"

cat >"$WORK/server.py" <<'PY'
import functools, http.server, sys
root = sys.argv[1]
class H(http.server.SimpleHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/latest":
            self.send_response(302)
            self.send_header("Location", "/tag/v9.9.9")
            self.end_headers()
        elif self.path.startswith("/tag/"):
            self.send_response(200)
            self.end_headers()
        else:
            super().do_GET()
    def log_message(self, *a):
        pass
srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), functools.partial(H, directory=root))
open(sys.argv[2], "w").write(str(srv.server_address[1]))
srv.serve_forever()
PY
python3 "$WORK/server.py" "$WORK/rel" "$WORK/port" &
SERVER_PID=$!
i=0
while [ ! -s "$WORK/port" ]; do
	i=$((i + 1))
	[ "$i" -lt 100 ] || {
		echo "fake server did not start" >&2
		exit 1
	}
	sleep 0.1
done
BASE="http://127.0.0.1:$(cat "$WORK/port")"

fail=0
# run <name> <expected rc> <expected output substring> [ENV=VALUE ...]
run() {
	name=$1 want_rc=$2 want_out=$3
	shift 3
	rc=0
	out=$(env "$@" EVE_WALLETS_RELEASE_BASE="$BASE" pwsh -NoProfile -NonInteractive -File "$ROOT/install.ps1" 2>&1) || rc=$?
	if [ "$rc" = "$want_rc" ] && printf '%s' "$out" | grep -q -- "$want_out"; then
		echo "ok   $name"
	else
		echo "FAIL $name (rc=$rc, want $want_rc, want output ~ '$want_out')"
		printf '%s\n' "$out" | sed 's/^/     /'
		fail=1
	fi
}

D="$WORK/inst"
run "latest release installs" 0 "installed" INSTALL_DIR="$D"
[ -f "$D/eve-wallets.exe" ] || { echo "FAIL exe missing"; fail=1; }
run "reinstall keeps previous binary" 0 "checksum ok" INSTALL_DIR="$D" VERSION=9.9.9
[ -f "$D/eve-wallets.exe.bak-prev" ] || { echo "FAIL bak-prev missing"; fail=1; }
ls "$D"/.eve-wallets.new.* >/dev/null 2>&1 && { echo "FAIL staged file left behind"; fail=1; }

D2="$WORK/inst-mismatch"
run "checksum mismatch aborts" 1 "checksum mismatch" INSTALL_DIR="$D2" VERSION=v8.8.8
[ ! -e "$D2/eve-wallets.exe" ] || { echo "FAIL installed despite mismatch"; fail=1; }

D3="$WORK/inst-arch"
run "unsupported architecture" 1 "unsupported architecture" INSTALL_DIR="$D3" EVE_WALLETS_TEST_ARCH=Arm64
[ ! -e "$D3/eve-wallets.exe" ] || { echo "FAIL installed on arm64"; fail=1; }

run "missing asset" 1 "download failed" INSTALL_DIR="$WORK/inst-missing" VERSION=v7.7.7

# A failed replacement must leave the installed exe and the older backup alone.
D4="$WORK/inst-locked"
run "seed install" 0 "installed" INSTALL_DIR="$D4"
printf 'older backup\n' >"$D4/eve-wallets.exe.bak-prev"
printf 'running exe\n' >"$D4/eve-wallets.exe"
run "failed replace keeps backup and exe" 1 "stop it" INSTALL_DIR="$D4" EVE_WALLETS_TEST_FAIL_REPLACE=1
[ "$(cat "$D4/eve-wallets.exe.bak-prev")" = "older backup" ] || { echo "FAIL old backup was overwritten"; fail=1; }
[ "$(cat "$D4/eve-wallets.exe")" = "running exe" ] || { echo "FAIL installed exe was not restored"; fail=1; }
ls "$D4"/.eve-wallets.* >/dev/null 2>&1 && { echo "FAIL temp file left behind"; fail=1; }

# iex mode must not kill the host shell and must still report the error.
rc=0
out=$(env INSTALL_DIR="$WORK/inst-iex" VERSION=v7.7.7 EVE_WALLETS_RELEASE_BASE="$BASE" \
	pwsh -NoProfile -NonInteractive -Command "Get-Content -Raw '$ROOT/install.ps1' | Invoke-Expression; 'host shell alive'" 2>&1) || rc=$?
if printf '%s' "$out" | grep -q "download failed" && printf '%s' "$out" | grep -q "host shell alive"; then
	echo "ok   iex mode survives a failure"
else
	echo "FAIL iex mode (rc=$rc)"
	printf '%s\n' "$out" | sed 's/^/     /'
	fail=1
fi

exit "$fail"
