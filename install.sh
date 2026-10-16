#!/bin/sh
# ollamacmf installer for macOS and Linux.
#
#   curl -fsSL https://raw.githubusercontent.com/infosave2007/ollamacmf/main/install.sh | sh
#
# Downloads the latest release archive for this machine, verifies its SHA-256
# against the published checksums, and installs `ollamacmf` plus the bundled
# Cortiq runtime into /usr/local/bin (or ~/.local/bin when that is not
# writable). Set OLLAMACMF_VERSION=cmf-v0.1.0 to pin a release, or
# OLLAMACMF_INSTALL_DIR to choose the destination.
set -eu

REPO="${OLLAMACMF_REPO:-infosave2007/ollamacmf}"
VERSION="${OLLAMACMF_VERSION:-latest}"

say() { printf '%s\n' "$*"; }
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "$1 is required"; }

need curl
need tar

os="$(uname -s)"
arch="$(uname -m)"
case "$os" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) fail "unsupported operating system: $os (use the Windows installer or build from source)" ;;
esac
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) fail "unsupported architecture: $arch" ;;
esac

if [ "$VERSION" = latest ]; then
  base="https://github.com/$REPO/releases/latest/download"
else
  base="https://github.com/$REPO/releases/download/$VERSION"
fi
archive="ollamacmf-$os-$arch.tar.gz"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

say "Downloading $archive ($VERSION)..."
curl -fsSL --retry 3 -o "$tmp/$archive" "$base/$archive" \
  || fail "could not download $base/$archive"
curl -fsSL --retry 3 -o "$tmp/checksums.txt" "$base/checksums.txt" \
  || fail "could not download the release checksums"

expected="$(grep " $archive\$" "$tmp/checksums.txt" | awk '{print $1}')"
[ -n "$expected" ] || fail "checksums.txt has no entry for $archive"
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$tmp/$archive" | awk '{print $1}')"
else
  actual="$(shasum -a 256 "$tmp/$archive" | awk '{print $1}')"
fi
[ "$actual" = "$expected" ] || fail "checksum mismatch for $archive"

mkdir -p "$tmp/unpacked"
tar -xzf "$tmp/$archive" -C "$tmp/unpacked"
[ -f "$tmp/unpacked/ollamacmf" ] || fail "archive does not contain the ollamacmf binary"

dest="${OLLAMACMF_INSTALL_DIR:-}"
sudo_cmd=""
if [ -z "$dest" ]; then
  if [ -w /usr/local/bin ]; then
    dest=/usr/local/bin
  elif command -v sudo >/dev/null 2>&1 && [ -t 0 ]; then
    dest=/usr/local/bin
    sudo_cmd=sudo
  else
    dest="$HOME/.local/bin"
  fi
fi
$sudo_cmd mkdir -p "$dest"

say "Installing to $dest..."
$sudo_cmd install -m 0755 "$tmp/unpacked/ollamacmf" "$dest/ollamacmf"
if [ -f "$tmp/unpacked/cortiq" ]; then
  $sudo_cmd install -m 0755 "$tmp/unpacked/cortiq" "$dest/cortiq"
else
  say "note: this platform has no bundled Cortiq runtime; ollamacmf will download one on first use"
fi

case ":$PATH:" in
  *":$dest:"*) ;;
  *) say "note: add $dest to your PATH, for example: export PATH=\"$dest:\$PATH\"" ;;
esac

say ""
say "Installed $("$dest/ollamacmf" --version 2>/dev/null | head -n1 || echo ollamacmf)."
say ""
say "Next steps:"
say "  ollamacmf serve                                              # start the server"
say "  ollamacmf run hf.co/infosave/LFM2.5-cmf:lfm2.5-230m-q4tp     # chat with a CMF model"
say "  ollamacmf cmf search                                         # browse available CMF models"
