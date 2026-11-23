#!/bin/sh
# Downloads the geo databases Xray's routing rules use (geoip:, geosite:) into $1.
#
# geoip.dat keeps only the countries the app and the server's configs use (24 MB -> about 1 MB).
# A rule for another country makes the app download the full file once (core/geo.go); keep
# GEOIP_CODES in step with BuiltinGeoIP there.
set -eu
dir=${1:-core/testdata/geo}
mkdir -p "$dir"
dir=$(cd "$dir" && pwd)
root=$(cd "$(dirname "$0")/.." && pwd)
GEOIP_CODES=private,ir,cn,ru
curl -fsSL -o "$dir/geoip.dat" https://github.com/v2fly/geoip/releases/latest/download/geoip.dat
curl -fsSL -o "$dir/geosite.dat" https://github.com/v2fly/domain-list-community/releases/latest/download/dlc.dat
(cd "$root/core" && go run ./cmd/geotrim -in "$dir/geoip.dat" -out "$dir/geoip.dat" "$GEOIP_CODES")
