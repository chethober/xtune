#!/bin/sh
# xtune installer for Linux, macOS and FreeBSD.
#   curl -fsSL https://raw.githubusercontent.com/chethober/xtune/main/install.sh | sh
# Environment: XTUNE_VERSION=v0.1.0 to pin a release, XTUNE_DIR=/path to choose the install dir.
set -eu

REPO=chethober/xtune

fail() { echo "xtune install: $*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "'$1' is required"; }
need curl
need tar

case $(uname -s) in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  FreeBSD) os=freebsd ;;
  *) fail "unsupported OS $(uname -s) (Windows: use install.ps1)" ;;
esac
case $(uname -m) in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) fail "unsupported architecture $(uname -m)" ;;
esac

version=${XTUNE_VERSION:-}
if [ -z "$version" ]; then
  version=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" |
    sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | sed -n 1p)
fi
[ -n "$version" ] || fail "could not determine the latest release"

file="xtune_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$version"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "downloading xtune $version ($os/$arch)"
curl -fsSL -o "$tmp/$file" "$base/$file" || fail "download failed: $base/$file"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || fail "download failed: checksums.txt"

want=$(grep " $file\$" "$tmp/checksums.txt" | cut -d' ' -f1)
if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "$tmp/$file" | cut -d' ' -f1)
else
  got=$(shasum -a 256 "$tmp/$file" | cut -d' ' -f1)
fi
if [ -z "$want" ] || [ "$want" != "$got" ]; then fail "checksum mismatch for $file"; fi

tar -xzf "$tmp/$file" -C "$tmp" xtune

dir=${XTUNE_DIR:-}
if [ -z "$dir" ]; then
  if [ -w /usr/local/bin ]; then dir=/usr/local/bin
  elif command -v sudo >/dev/null 2>&1 && [ -t 0 ]; then dir=/usr/local/bin; use_sudo=1
  else dir="$HOME/.local/bin"
  fi
fi
mkdir -p "$dir" 2>/dev/null || true
if [ "${use_sudo:-0}" = 1 ]; then
  sudo install -m 755 "$tmp/xtune" "$dir/xtune"
else
  install -m 755 "$tmp/xtune" "$dir/xtune"
fi

echo "installed $dir/xtune"
case ":$PATH:" in
  *":$dir:"*) ;;
  *) echo "note: $dir is not on your PATH - add it, e.g. export PATH=\"$dir:\$PATH\"" ;;
esac
echo "run: xtune"
