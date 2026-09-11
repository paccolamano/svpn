#!/bin/sh
#
# Download the latest svpn release and install it.
#
#   curl -fsSL https://raw.githubusercontent.com/paccolamano/svpn/main/install.sh | sh
#
# This script is deliberately thin. It resolves a release, verifies the archive
# against its published checksum, unpacks it and then hands over to
# "svpnd install" — which is where every decision about groups, the systemd
# unit and the uid allowlist actually lives. Nothing about the installation is
# duplicated here, because two copies of that knowledge would drift.
#
# SVPN_VERSION pins a tag instead of taking the latest.

set -eu

REPO="${SVPN_REPO:-paccolamano/svpn}"
VERSION="${SVPN_VERSION:-}"

fail() {
	echo "svpn: $*" >&2
	exit 1
}

# --- what we are running on ---------------------------------------------------

[ "$(uname -s)" = "Linux" ] || fail "the daemon only runs on Linux; on macOS and Windows the protocol works but netcfg and ipc are not implemented yet"

case "$(uname -m)" in
x86_64 | amd64) ARCH=amd64 ;;
aarch64 | arm64) ARCH=arm64 ;;
*) fail "no release is built for $(uname -m)" ;;
esac

# --- how we fetch -------------------------------------------------------------

if command -v curl >/dev/null 2>&1; then
	fetch() { curl -fsSL "$1" -o "$2"; }
	read_url() { curl -fsSL "$1"; }
elif command -v wget >/dev/null 2>&1; then
	fetch() { wget -qO "$2" "$1"; }
	read_url() { wget -qO- "$1"; }
else
	fail "neither curl nor wget is installed"
fi

command -v tar >/dev/null 2>&1 || fail "tar is not installed"
command -v sha256sum >/dev/null 2>&1 || fail "sha256sum is not installed"

# The install itself needs root: it creates a group, writes under /usr/local
# and /etc, and talks to systemd.
if [ "$(id -u)" -eq 0 ]; then
	SUDO=""
elif command -v sudo >/dev/null 2>&1; then
	SUDO="sudo"
else
	fail "this needs root and sudo is not installed; re-run it as root"
fi

# --- which release ------------------------------------------------------------

if [ -z "$VERSION" ]; then
	# The API answer is JSON and this is POSIX sh, so the tag is pulled out with
	# sed rather than parsed. It is one well-known field, and pulling in jq to
	# read it would be a dependency for no benefit.
	VERSION=$(read_url "https://api.github.com/repos/$REPO/releases/latest" |
		sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' |
		head -n 1)
	[ -n "$VERSION" ] || fail "could not work out the latest release of $REPO; set SVPN_VERSION to a tag"
fi

# Archive names carry the version without its leading v, as goreleaser writes them.
NUMBER="${VERSION#v}"
ARCHIVE="svpn_${NUMBER}_linux_${ARCH}.tar.gz"
BASE="https://github.com/$REPO/releases/download/$VERSION"

# --- download, verify, install ------------------------------------------------

WORKDIR=$(mktemp -d)
trap 'rm -rf "$WORKDIR"' EXIT INT TERM

echo "svpn: downloading $VERSION for linux/$ARCH"
fetch "$BASE/$ARCHIVE" "$WORKDIR/$ARCHIVE" || fail "could not download $BASE/$ARCHIVE"
fetch "$BASE/SHA256SUMS" "$WORKDIR/SHA256SUMS" || fail "could not download $BASE/SHA256SUMS"

# The checksum catches a download that truncated or was corrupted in transit,
# which matters because what follows is unpacked over binaries that run as
# root. It does not prove where the archive came from: it and SHA256SUMS are
# published together. For that, verify the build attestation:
#
#   gh attestation verify "$ARCHIVE" --repo paccolamano/svpn
echo "svpn: verifying the checksum"
(cd "$WORKDIR" && grep " [ *]\{0,1\}$ARCHIVE\$" SHA256SUMS | sha256sum -c -) >/dev/null ||
	fail "$ARCHIVE does not match its published checksum"

tar -xzf "$WORKDIR/$ARCHIVE" -C "$WORKDIR" || fail "could not unpack $ARCHIVE"
[ -x "$WORKDIR/svpnd" ] || fail "$ARCHIVE does not contain svpnd"

echo "svpn: installing"
$SUDO "$WORKDIR/svpnd" install "$@"
