#!/bin/sh
# Builds the desktop app with its engine: scripts/build-desktop.sh linux|windows VERSION
set -eu
os=$1
version=${2:-dev}
root=$(cd "$(dirname "$0")/.." && pwd)
ext=""
[ "$os" = windows ] && ext=.exe
(cd "$root/app" && flutter build "$os" --release --build-name="${version#v}")
if [ "$os" = linux ]; then
  out="$root/app/build/linux/x64/release/bundle"
else
  out="$root/app/build/windows/x64/runner/Release"
fi
(cd "$root/core" && CGO_ENABLED=0 GOOS=$os GOARCH=amd64 go build -trimpath \
  -ldflags "-s -w -X github.com/chethober/xtune-client/core.Version=$version" -o "$out/xtune-core$ext" ./cmd/xtune-core)
"$root/scripts/fetch-geo.sh" "$out"
echo "$out"
