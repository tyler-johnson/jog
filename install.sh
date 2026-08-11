#!/bin/sh
# jog installer for linux/macOS:
#   curl -fsSL https://raw.githubusercontent.com/tyler-johnson/jog/main/install.sh | sh
#
# Downloads the latest release binary for this platform, verifies its
# sha256 against the release's checksums.txt, and installs it to
# ~/.local/bin (override with JOG_INSTALL_DIR; pin a version with
# JOG_VERSION, e.g. JOG_VERSION=v1.3.0). Windows: use install.ps1.
set -eu

REPO="tyler-johnson/jog"
INSTALL_DIR="${JOG_INSTALL_DIR:-$HOME/.local/bin}"

case "$(uname -s)" in
  Linux)  os=linux ;;
  Darwin) os=darwin ;;
  *) echo "jog installer: unsupported OS $(uname -s) — on Windows use install.ps1; elsewhere: go install github.com/$REPO/cmd/jog@latest" >&2
     exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64)  arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "jog installer: unsupported architecture $(uname -m) — try: go install github.com/$REPO/cmd/jog@latest" >&2
     exit 1 ;;
esac

# fetch <url> <dest>: curl or wget, whichever exists.
if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL -o "$2" "$1"; }
  # The releases/latest page redirects to .../tag/<version> — the header
  # names the version without touching the rate-limited API.
  latest() { curl -fsSI "https://github.com/$REPO/releases/latest" | tr -d '\r' | sed -n 's/^[Ll]ocation:.*\/tag\///p'; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -q -O "$2" "$1"; }
  latest() { wget -q -S --max-redirect=0 -O /dev/null "https://github.com/$REPO/releases/latest" 2>&1 | tr -d '\r' | sed -n 's/^ *[Ll]ocation:.*\/tag\///p' | head -n1; }
else
  echo "jog installer: needs curl or wget" >&2
  exit 1
fi

version="${JOG_VERSION:-$(latest)}"
if [ -z "$version" ]; then
  echo "jog installer: could not determine the latest release — set JOG_VERSION=vX.Y.Z and re-run" >&2
  exit 1
fi

archive="jog_${version#v}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$version"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "downloading jog $version ($os/$arch)…"
fetch "$base/$archive" "$tmp/$archive"
fetch "$base/checksums.txt" "$tmp/checksums.txt"

want="$(awk -v f="$archive" '$2 == f {print $1}' "$tmp/checksums.txt")"
if [ -z "$want" ]; then
  echo "jog installer: checksums.txt has no entry for $archive" >&2
  exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
  got="$(sha256sum "$tmp/$archive" | awk '{print $1}')"
else
  got="$(shasum -a 256 "$tmp/$archive" | awk '{print $1}')"
fi
if [ "$got" != "$want" ]; then
  echo "jog installer: checksum mismatch for $archive — refusing to install" >&2
  exit 1
fi

tar -xzf "$tmp/$archive" -C "$tmp" jog
mkdir -p "$INSTALL_DIR"
if command -v install >/dev/null 2>&1; then
  install -m 0755 "$tmp/jog" "$INSTALL_DIR/jog"
else
  cp "$tmp/jog" "$INSTALL_DIR/jog" && chmod 0755 "$INSTALL_DIR/jog"
fi

echo "installed jog $version to $INSTALL_DIR/jog"
case ":$PATH:" in
  *:"$INSTALL_DIR":*) ;;
  *) echo ""
     echo "$INSTALL_DIR is not on your PATH — add it to your shell rc:"
     echo "  export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
esac
echo ""
echo "next steps:"
echo "  jog install     # guided setup: the git alias, agent hooks, editor hooks"
echo "  jog doctor      # verify the wiring"
