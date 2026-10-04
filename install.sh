#!/bin/sh
# eve-wallets installer for Linux and macOS.
#
#   curl -fsSL https://raw.githubusercontent.com/escorbuto-petoruti/eve-wallets/main/install.sh | sh
#   sh install.sh [--systemd]
#
# Environment:
#   VERSION                   release tag to install (default: the latest release)
#   INSTALL_DIR               where to put the binary (default: $HOME/.local/bin)
#   EVE_WALLETS_RELEASE_BASE  releases URL (default: the GitHub releases page);
#                             <base>/latest redirects to <base>/tag/<tag> and
#                             assets live under <base>/download/<tag>/
#
# --systemd also installs and starts a systemd user service (Linux only).
# The installer never uses sudo.
set -eu

REPO_BASE=${EVE_WALLETS_RELEASE_BASE:-https://github.com/escorbuto-petoruti/eve-wallets/releases}
INSTALL_DIR=${INSTALL_DIR:-$HOME/.local/bin}
WITH_SYSTEMD=0

die() {
	echo "install.sh: $*" >&2
	exit 1
}

for arg in "$@"; do
	case $arg in
	--systemd) WITH_SYSTEMD=1 ;;
	-h | --help)
		echo "usage: sh install.sh [--systemd]   (env: VERSION, INSTALL_DIR, EVE_WALLETS_RELEASE_BASE)"
		exit 0
		;;
	*) die "unknown argument: $arg (supported: --systemd)" ;;
	esac
done

[ -n "${HOME:-}" ] || die "HOME is not set"
command -v curl >/dev/null 2>&1 || die "curl is required"
command -v tar >/dev/null 2>&1 || die "tar is required"

case $(uname -s) in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) die "unsupported OS $(uname -s): on Windows download the zip from the releases page and see the README" ;;
esac
case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) die "unsupported architecture $(uname -m) (supported: amd64, arm64)" ;;
esac

if command -v sha256sum >/dev/null 2>&1; then
	sha256_of() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
	sha256_of() { shasum -a 256 "$1" | awk '{print $1}'; }
else
	die "sha256sum or shasum is required to verify the download"
fi

tag=${VERSION:-}
if [ -z "$tag" ]; then
	echo "looking up the latest release"
	final=$(curl -fsSL -o /dev/null -w '%{url_effective}' "$REPO_BASE/latest") ||
		die "could not look up the latest release at $REPO_BASE/latest"
	tag=${final##*/}
	case $tag in
	v[0-9]*) ;;
	*) die "could not tell the latest release from $final (is there a published release?)" ;;
	esac
fi
case $tag in
v[0-9]*) ;;
*) tag=v$tag ;;
esac
version=${tag#v}
archive="eve-wallets_${version}_${os}_${arch}.tar.gz"
url="$REPO_BASE/download/$tag"

tmp=$(mktemp -d) || die "could not create a temporary directory"
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "downloading $archive ($tag)"
curl -fsSL -o "$tmp/$archive" "$url/$archive" || die "download failed: $url/$archive"
curl -fsSL -o "$tmp/checksums.txt" "$url/checksums.txt" || die "download failed: $url/checksums.txt"

want=$(awk -v f="$archive" '$2 == f || $2 == "*" f {print $1; exit}' "$tmp/checksums.txt")
[ -n "$want" ] || die "checksums.txt has no entry for $archive"
got=$(sha256_of "$tmp/$archive")
[ "$got" = "$want" ] || die "checksum mismatch for $archive (expected $want, got $got): nothing was installed"
echo "checksum ok"

tar -xzf "$tmp/$archive" -C "$tmp" eve-wallets || die "the archive does not contain eve-wallets"
[ -f "$tmp/eve-wallets" ] || die "the archive does not contain eve-wallets"

mkdir -p "$INSTALL_DIR" || die "could not create $INSTALL_DIR"
target="$INSTALL_DIR/eve-wallets"
new="$INSTALL_DIR/.eve-wallets.new.$$"
cp "$tmp/eve-wallets" "$new" || die "could not write to $INSTALL_DIR"
chmod 0755 "$new"
if [ -e "$target" ]; then
	cp -p "$target" "$target.bak-prev" || {
		rm -f "$new"
		die "could not keep the previous binary as $target.bak-prev"
	}
fi
mv -f "$new" "$target" || {
	rm -f "$new"
	die "could not install $target"
}
echo "installed $target ($version)"

if [ "$WITH_SYSTEMD" = 1 ]; then
	[ "$os" = linux ] || die "--systemd is only available on Linux"
	command -v systemctl >/dev/null 2>&1 || die "--systemd needs systemctl"
	unit_dir=${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user
	mkdir -p "$unit_dir"
	cat >"$unit_dir/eve-wallets.service" <<UNIT
[Unit]
Description=eve-wallets

[Service]
ExecStart=$target serve
Restart=on-failure

[Install]
WantedBy=default.target
UNIT
	systemctl --user daemon-reload
	systemctl --user enable --now eve-wallets
	echo "systemd user service installed and started: systemctl --user status eve-wallets"
fi

echo
echo "Next steps:"
case ":$PATH:" in
*":$INSTALL_DIR:"*) ;;
*) echo "  - add $INSTALL_DIR to your PATH, e.g. export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
esac
echo "  - run: eve-wallets serve, then open http://localhost:8088 and sign in with EVE SSO"
if [ "$WITH_SYSTEMD" = 0 ] && [ "$os" = linux ]; then
	echo "  - to run it in the background, re-run this installer with --systemd, or see the README"
fi
echo "  - to update later: eve-wallets update"
