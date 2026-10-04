#!/bin/sh
# Build and package eve-wallets release assets.
#
#   scripts/package.sh <version> [outdir]
#
# <version> is the release version without the leading "v" (for example 1.2.3).
# It writes, into outdir (default: dist), one archive per platform named
# eve-wallets_<version>_<os>_<arch>.tar.gz (.zip for windows), each holding the
# binary, LICENSE and README.md, plus checksums.txt in sha256sum format.
#
# PLATFORMS overrides the platform list (space separated os/arch pairs).
set -eu

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
	echo "usage: $0 <version> [outdir]" >&2
	exit 2
fi
VERSION=${1#v}
OUT=${2:-dist}
PLATFORMS=${PLATFORMS:-"linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64"}

ROOT=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

if command -v sha256sum >/dev/null 2>&1; then
	sha256() { sha256sum "$@"; }
else
	sha256() { shasum -a 256 "$@"; }
fi

make_zip() { # make_zip <archive> <dir> <files...>
	archive=$1 dir=$2
	shift 2
	if command -v zip >/dev/null 2>&1; then
		(cd "$dir" && zip -q -X "$archive" "$@")
	else
		python3 - "$archive" "$dir" "$@" <<'PY'
import sys, zipfile
archive, directory, files = sys.argv[1], sys.argv[2], sys.argv[3:]
with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED) as z:
    for name in files:
        z.write(directory + "/" + name, name)
PY
	fi
}

rm -f "$OUT/checksums.txt"
for platform in $PLATFORMS; do
	os=${platform%/*}
	arch=${platform#*/}
	bin=eve-wallets
	[ "$os" = windows ] && bin=eve-wallets.exe
	stage="$WORK/$os-$arch"
	mkdir -p "$stage"
	echo "building $os/$arch"
	(cd "$ROOT" && CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
		-ldflags "-s -w -X main.version=$VERSION" -o "$stage/$bin" ./cmd/eve-wallets)
	cp "$ROOT/LICENSE" "$ROOT/README.md" "$stage/"
	if [ "$os" = windows ]; then
		archive="eve-wallets_${VERSION}_${os}_${arch}.zip"
		rm -f "$OUT/$archive"
		make_zip "$OUT/$archive" "$stage" "$bin" LICENSE README.md
	else
		archive="eve-wallets_${VERSION}_${os}_${arch}.tar.gz"
		# GNU tar can drop the local owner; bsdtar (macOS) uses other flags.
		if tar --version 2>/dev/null | grep -q 'GNU tar'; then
			tar --owner=0 --group=0 --numeric-owner -C "$stage" -czf "$OUT/$archive" "$bin" LICENSE README.md
		else
			tar -C "$stage" -czf "$OUT/$archive" "$bin" LICENSE README.md
		fi
	fi
done

(cd "$OUT" && for f in eve-wallets_"${VERSION}"_*; do sha256 "$f"; done >checksums.txt)
echo "wrote $(wc -l <"$OUT/checksums.txt" | tr -d ' ') archives and checksums.txt to $OUT"
