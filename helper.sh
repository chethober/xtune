#!/usr/bin/env bash
# Server-side helper for xtune. Runs as root. Everything lives in $D; nothing else is touched
# except the xray-autotune systemd units and firewall rules this script opens itself.
set -euo pipefail
D=/opt/xray-autotune
X=$D/xray
cmd=${1:-}; shift || true

fw_open() {
  local f=$1; shift
  for spec in "$@"; do
    if command -v ufw >/dev/null && ufw status 2>/dev/null | grep '^Status: active' >/dev/null; then
      ufw allow "$spec" >/dev/null && echo "ufw $spec" >> "$f"
    fi
    if command -v firewall-cmd >/dev/null && firewall-cmd --state >/dev/null 2>&1; then
      firewall-cmd --add-port="$spec" >/dev/null && echo "firewalld $spec" >> "$f"
    fi
  done
}
fw_close() {
  local f=$1
  [ -f "$f" ] || return 0
  while read -r kind spec; do
    case $kind in
      ufw) ufw delete allow "$spec" >/dev/null 2>&1 || true ;;
      firewalld) firewall-cmd --remove-port="$spec" >/dev/null 2>&1 || true ;;
    esac
  done < "$f"
  rm -f "$f"
}

case $cmd in
status)
  echo "ARCH $(uname -m)"
  if [ -x $X ]; then echo "XRAY $($X version | sed -n 1p | awk '{print $2}')"; else echo "XRAY none"; fi
  echo "UNIT $(systemctl is-active xray-autotune 2>/dev/null || true)"
  if [ -f $D/state.json ]; then echo "STATE yes"; else echo "STATE no"; fi
  if [ -x /usr/local/x-ui/x-ui ]; then
    echo "XUI yes"
    /usr/local/x-ui/x-ui setting -show 2>/dev/null | awk -F': ' '/^port:/{print "XUI_PORT", $2} /^webBasePath:/{print "XUI_BASE", $2}'
    if [ -f /etc/x-ui/install-result.env ]; then sed -n 's/^XUI_API_TOKEN=/XUI_TOKEN /p' /etc/x-ui/install-result.env; fi
    for b in /usr/local/x-ui/bin/xray-linux-*; do [ -x "$b" ] && echo "XUI_XRAY $("$b" version | sed -n 1p | awk '{print $2}')" && break; done
    echo "XUI_UNIT $(systemctl is-active x-ui 2>/dev/null || true)"
  else
    echo "XUI no"
  fi ;;
latest)
  # Xray publishes most releases as pre-releases; "stable" uses GitHub's latest non-prerelease
  if [ "${1:-}" = stable ]; then url=https://api.github.com/repos/XTLS/Xray-core/releases/latest
  else url='https://api.github.com/repos/XTLS/Xray-core/releases?per_page=5'; fi
  curl -fsSL -m 20 "$url" | grep -o '"tag_name": *"[^"]*"' | sed -n '1s/.*"\(v[^"]*\)"$/\1/p' ;;
install)
  ver=$1
  mkdir -p $D && chmod 700 $D
  case $(uname -m) in
    x86_64) a=64 ;; aarch64|arm64) a=arm64-v8a ;; armv7l) a=arm32-v7a ;; i686|i386) a=32 ;;
    *) echo "unsupported arch $(uname -m)" >&2; exit 1 ;;
  esac
  b=https://github.com/XTLS/Xray-core/releases/download/$ver
  t=$(mktemp -d); trap 'rm -rf "$t"' EXIT
  curl -fsSL -m 180 -o "$t/x.zip" "$b/Xray-linux-$a.zip"
  curl -fsSL -m 30 -o "$t/x.dgst" "$b/Xray-linux-$a.zip.dgst"
  want=$(awk -F'= ' '/^SHA2-256/{print $2}' "$t/x.dgst"); got=$(sha256sum "$t/x.zip" | cut -d' ' -f1)
  if [ -z "$want" ] || [ "$want" != "$got" ]; then echo "checksum mismatch ($got != $want)" >&2; exit 1; fi
  if command -v unzip >/dev/null; then unzip -qo "$t/x.zip" -d "$t/u"
  else python3 -c 'import sys,zipfile; zipfile.ZipFile(sys.argv[1]).extractall(sys.argv[2])' "$t/x.zip" "$t/u"; fi
  install -m 755 "$t/u/xray" $X.new && mv -f $X.new $X
  cp "$t"/u/*.dat $D/ 2>/dev/null || true
  if systemctl is-active xray-autotune >/dev/null 2>&1; then systemctl restart xray-autotune; fi
  $X version | sed -n 1p ;;
probe)
  for h in "$@"; do
    echo "SNI $h $(curl -so /dev/null --tlsv1.3 --http2 -m 4 -w '%{http_version} %{time_appconnect}' "https://$h/" 2>/dev/null || echo fail)"
  done
  ss -Htuln | awk '{print "USED", $1, $5}' ;;
start)
  systemctl stop xray-autotune-test 2>/dev/null || true
  systemctl reset-failed xray-autotune-test 2>/dev/null || true
  if ! $X run -test -c $D/test.json >/dev/null 2>&1; then $X run -test -c $D/test.json 2>&1 | tail -5; exit 1; fi
  fw_open $D/fw.test "$@"
  systemd-run --quiet --unit=xray-autotune-test --property=LimitNOFILE=65535 --property=RuntimeMaxSec=7200 $X run -c $D/test.json
  sleep 1; systemctl is-active xray-autotune-test ;;
stop-test)
  systemctl stop xray-autotune-test 2>/dev/null || true
  systemctl reset-failed xray-autotune-test 2>/dev/null || true
  fw_close $D/fw.test
  rm -rf $D/test.json $D/test ;;
apply)
  $X run -test -c $D/config.json >/dev/null
  if [ ! -f /etc/systemd/system/xray-autotune.service ]; then
    cat > /etc/systemd/system/xray-autotune.service <<EOF
[Unit]
Description=Xray (managed by xtune)
After=network-online.target
Wants=network-online.target
[Service]
ExecStart=$X run -c $D/config.json
Restart=on-failure
LimitNOFILE=65535
[Install]
WantedBy=multi-user.target
EOF
    systemctl daemon-reload
    systemctl enable xray-autotune >/dev/null 2>&1
  fi
  systemctl restart xray-autotune
  fw_close $D/fw.deployed; fw_open $D/fw.deployed "$@"
  sleep 1; systemctl is-active xray-autotune ;;
cleanup)
  # remove test leftovers; the whole dir goes only if nothing is deployed
  systemctl stop xray-autotune-test 2>/dev/null || true
  systemctl reset-failed xray-autotune-test 2>/dev/null || true
  fw_close $D/fw.test
  if [ -f /etc/systemd/system/xray-autotune.service ]; then rm -rf $D/test.json $D/test; echo kept-deployed
  else rm -rf $D; echo removed; fi ;;
uninstall)
  systemctl disable --now xray-autotune xray-autotune-test >/dev/null 2>&1 || true
  rm -f /etc/systemd/system/xray-autotune.service; systemctl daemon-reload
  fw_close $D/fw.test; fw_close $D/fw.deployed
  rm -rf $D; echo uninstalled ;;
*)
  echo "usage: helper.sh status|latest|install|probe|start|stop-test|apply|cleanup|uninstall" >&2; exit 2 ;;
esac
