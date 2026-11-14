#!/bin/sh
# Builds the Go core as an Android library (needs the Android SDK and NDK):
#   scripts/build-android-core.sh VERSION
set -eu
version=${1:-dev}
root=$(cd "$(dirname "$0")/.." && pwd)
command -v gomobile >/dev/null || go install golang.org/x/mobile/cmd/gomobile@latest
command -v gomobile >/dev/null || export PATH="$(go env GOPATH)/bin:$PATH"
gomobile init
mkdir -p "$root/app/android/app/libs" "$root/app/android/app/src/main/assets/geo"
(cd "$root/core" && gomobile bind -target=android/arm64,android/arm,android/amd64 -androidapi 21 -trimpath \
  -ldflags "-s -w -X github.com/chethober/xtune-client/core.Version=$version" \
  -o "$root/app/android/app/libs/xtune-core.aar" ./mobile)
"$root/scripts/fetch-geo.sh" "$root/app/android/app/src/main/assets/geo"
