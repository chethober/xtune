#!/bin/sh
# Downloads the geo databases Xray's routing rules use (geoip:, geosite:) into $1.
set -eu
dir=${1:-core/testdata/geo}
mkdir -p "$dir"
curl -fsSL -o "$dir/geoip.dat" https://github.com/v2fly/geoip/releases/latest/download/geoip.dat
curl -fsSL -o "$dir/geosite.dat" https://github.com/v2fly/domain-list-community/releases/latest/download/dlc.dat
