#!/bin/sh
# Install gosha from a GitHub release:
#
#   curl -fsSL https://raw.githubusercontent.com/galimru/gosha/main/install.sh | sh
#   curl -fsSL …/install.sh | sh -s -- --version v0.3.1 --prefix /opt/bin
#
# It downloads the release for this OS and architecture, verifies it against
# the release's checksums.txt, and installs gosha into ~/.local/bin
# (/usr/local/bin as root). Needs sh, curl or wget, tar, and sha256sum or
# shasum.
set -eu

repo=galimru/gosha
api=${GOSHA_RELEASES_API:-https://api.github.com/repos/$repo}
download=${GOSHA_RELEASES_DOWNLOAD:-https://github.com/$repo/releases/download}
version=""
prefix=""

die() {
	echo "install.sh: $*" >&2
	exit 1
}

usage() {
	echo "usage: install.sh [--version vX.Y.Z] [--prefix DIR]"
}

while [ $# -gt 0 ]; do
	case $1 in
	--version)
		[ $# -ge 2 ] || die "--version needs a value"
		version=$2
		shift 2
		;;
	--prefix)
		[ $# -ge 2 ] || die "--prefix needs a value"
		prefix=$2
		shift 2
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		usage >&2
		exit 2
		;;
	esac
done

case $(uname -s) in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) die "unsupported OS $(uname -s): Gosha runs on Linux and macOS" ;;
esac
case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) die "unsupported architecture $(uname -m): Gosha runs on amd64 and arm64" ;;
esac

if command -v curl >/dev/null 2>&1; then
	fetch() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
	fetch() { wget -qO "$2" "$1"; }
else
	die "needs curl or wget"
fi
if command -v sha256sum >/dev/null 2>&1; then
	sha256() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
	sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
	die "needs sha256sum or shasum"
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

if [ -z "$version" ]; then
	fetch "$api/releases/latest" "$tmp/latest.json" || die "can't look up the latest release"
	version=$(sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' "$tmp/latest.json" | head -n 1)
	[ -n "$version" ] || die "can't read the latest release's tag"
fi

asset="gosha_${version#v}_${os}_${arch}.tar.gz"
fetch "$download/$version/checksums.txt" "$tmp/checksums.txt" || die "no release $version"
fetch "$download/$version/$asset" "$tmp/$asset" || die "release $version has no $asset"
want=$(awk -v a="$asset" '$2 == a || $2 == "*" a { print $1 }' "$tmp/checksums.txt")
[ -n "$want" ] || die "checksums.txt has no entry for $asset"
[ "$(sha256 "$tmp/$asset")" = "$want" ] || die "$asset: checksum mismatch: the download is corrupt or was tampered with"

tar -xzf "$tmp/$asset" -C "$tmp" gosha

if [ -z "$prefix" ]; then
	if [ "$(id -u)" = 0 ]; then
		prefix=/usr/local/bin
	else
		prefix=$HOME/.local/bin
	fi
fi
mkdir -p "$prefix"
# Written next to the target, then renamed over it, so a running gosha is
# never left with a half-written binary.
cp "$tmp/gosha" "$prefix/.gosha.new"
chmod 755 "$prefix/.gosha.new"
mv -f "$prefix/.gosha.new" "$prefix/gosha"

echo "installed gosha $version to $prefix/gosha"
case ":$PATH:" in
*":$prefix:"*) ;;
*) echo "note: $prefix is not on your PATH; add it, e.g. in ~/.profile: export PATH=\"$prefix:\$PATH\"" ;;
esac
