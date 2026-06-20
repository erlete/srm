#!/usr/bin/env bash
# srm one-line installer. The only supported target is Ubuntu x64.
#
#   curl -fsSL https://raw.githubusercontent.com/erlete/srm/stable/install.sh | sudo bash
#
# It downloads the latest published release artifact, verifies its sha256, and
# installs it to /usr/local/bin/srm. Override via environment:
#   SRM_VERSION=v1.2.0   install a specific tag (default: latest release)
#   PREFIX=/usr/local    install prefix (binary goes to $PREFIX/bin/srm)
set -euo pipefail

REPO="erlete/srm"
PREFIX="${PREFIX:-/usr/local}"
BINDIR="$PREFIX/bin"
VERSION="${SRM_VERSION:-}"

err() { echo "srm-install: $*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || err "required tool '$1' not found"; }

# Platform guard: srm assumes systemd + apt + cgroup v2 (Ubuntu x64).
os=$(uname -s); arch=$(uname -m)
[ "$os" = "Linux" ] || err "unsupported OS '$os' (srm targets Ubuntu x64 only)"
case "$arch" in
  x86_64 | amd64) ;;
  *) err "unsupported arch '$arch' (srm targets x86_64)" ;;
esac

need curl
need sha256sum
need install

# Resolve the version: the latest release tag unless pinned via SRM_VERSION.
if [ -z "$VERSION" ]; then
  VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" |
    grep -m1 '"tag_name"' | cut -d'"' -f4)
  [ -n "$VERSION" ] || err "could not resolve the latest release tag from GitHub"
fi

asset="srm-$VERSION-ubuntu-x64"
base="https://github.com/$REPO/releases/download/$VERSION"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "srm-install: downloading $asset"
curl -fsSL "$base/$asset" -o "$tmp/srm"
curl -fsSL "$base/$asset.sha256" -o "$tmp/srm.sha256"

# Verify integrity. The .sha256 names the release asset, so compare by hash value
# rather than relying on the filename matching our temp copy.
echo "srm-install: verifying checksum"
want=$(cut -d' ' -f1 "$tmp/srm.sha256")
have=$(sha256sum "$tmp/srm" | cut -d' ' -f1)
[ -n "$want" ] || err "could not read the published checksum"
[ "$want" = "$have" ] || err "checksum mismatch (want $want, got $have) - aborting"

# Install. Use sudo for the privileged steps only when the bindir is not writable
# and we are not already root (so 'curl ... | sudo bash' and a sudo-capable user
# both work).
sudo=""
if [ ! -w "$BINDIR" ] && [ "$(id -u)" -ne 0 ]; then
  need sudo
  sudo="sudo"
fi
$sudo install -d "$BINDIR"
$sudo install -m 0755 "$tmp/srm" "$BINDIR/srm"

installed=$("$BINDIR/srm" version 2>/dev/null || echo "srm")
echo "srm-install: installed $installed to $BINDIR/srm"
echo "next: run 'sudo srm' (first run guides setup), or 'sudo srm init'"
