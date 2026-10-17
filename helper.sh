#!/usr/bin/env bash
# Server-side helper for xtune. Runs as root. Everything lives in $D; nothing else is touched
# except the xray-autotune systemd units, the /usr/local/bin/xtune link and firewall rules this
# script opens itself.
set -euo pipefail
D=/opt/xray-autotune
X=$D/xray
XT=$D/xtune
LINK=/usr/local/bin/xtune
cmd=${1:-}; shift || true

# hop_rule adds (-A), checks (-C) or deletes (-D) the NAT rule that forwards a UDP port range to
# one port, for Hysteria2 port hopping. Rules carry the comment "xtune".
hop_rule() {
  local op=$1 ipt=$2 range=$3 port=$4
  $ipt -t nat "$op" PREROUTING -p udp --dport "$range" -m comment --comment xtune -j REDIRECT --to-ports "$port" 2>/dev/null
}
fw_open() {
  local f=$1; shift
  for spec in "$@"; do
    case $spec in
      hop:*)
        # hop:LO-HI:PORT; the firewall sees the forwarded port, which is opened on its own
        IFS=: read -r _ range port <<< "$spec"; range=${range/-/:}
        for ipt in iptables ip6tables; do
          if command -v $ipt >/dev/null && hop_rule -A $ipt "$range" "$port"; then echo "$ipt $range $port" >> "$f"; fi
        done
        continue ;;
    esac
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
  while read -r kind spec extra; do
    case $kind in
      ufw) ufw delete allow "$spec" >/dev/null 2>&1 || true ;;
      firewalld) firewall-cmd --remove-port="$spec" >/dev/null 2>&1 || true ;;
      iptables|ip6tables) while hop_rule -D "$kind" "$spec" "$extra"; do :; done ;;
    esac
  done < "$f"
  rm -f "$f"
}
# hop_restore re-adds recorded hop rules that are missing (they do not survive a reboot)
hop_restore() {
  local f=$1
  [ -f "$f" ] || return 0
  while read -r kind spec extra; do
    case $kind in
      iptables|ip6tables) hop_rule -C "$kind" "$spec" "$extra" || hop_rule -A "$kind" "$spec" "$extra" || true ;;
    esac
  done < "$f"
}
# link puts the server copy of xtune on PATH, unless another xtune install is already there
link() {
  if [ -L $LINK ] || [ ! -e $LINK ]; then ln -sfn $XT $LINK; else echo "note: $LINK is not xtune's link; left as is" >&2; fi
}
unlink_xt() {
  if [ -L $LINK ] && [ "$(readlink $LINK)" = $XT ]; then rm -f $LINK; fi
}
U=/etc/systemd/system
# the enforce timer: traffic accounting, quotas, expiry and device limits, every minute
install_timer() {
  [ -x $XT ] || return 0
  cat > $U/xray-autotune-enforce.service <<EOF
[Unit]
Description=xtune: usage, quotas, expiry and device limits
After=xray-autotune.service
[Service]
Type=oneshot
ExecStart=$XT enforce
EOF
  cat > $U/xray-autotune-enforce.timer <<EOF
[Unit]
Description=Run xtune enforce every minute
[Timer]
OnBootSec=1min
OnUnitActiveSec=1min
AccuracySec=5s
[Install]
WantedBy=timers.target
EOF
  systemctl daemon-reload
  systemctl enable --now xray-autotune-enforce.timer >/dev/null 2>&1
}
remove_units() {
  systemctl disable --now xray-autotune xray-autotune-test xray-autotune-sub xray-autotune-enforce.timer >/dev/null 2>&1 || true
  rm -f $U/xray-autotune.service $U/xray-autotune-sub.service $U/xray-autotune-enforce.service $U/xray-autotune-enforce.timer
  systemctl daemon-reload
}

case $cmd in
status)
  echo "ARCH $(uname -m)"
  if [ -x $X ]; then echo "XRAY $($X version | sed -n 1p | awk '{print $2}')"; else echo "XRAY none"; fi
  echo "UNIT $(systemctl is-active xray-autotune 2>/dev/null || true)"
  if [ -f $D/state.json ]; then echo "STATE yes"; else echo "STATE no"; fi
  echo "SUB $(systemctl is-active xray-autotune-sub 2>/dev/null || true)"
  echo "TIMER $(systemctl is-active xray-autotune-enforce.timer 2>/dev/null || true)"
  if command -v ufw >/dev/null && ufw status 2>/dev/null | grep '^Status: active' >/dev/null; then echo "FW ufw"
  elif command -v firewall-cmd >/dev/null && firewall-cmd --state >/dev/null 2>&1; then echo "FW firewalld"
  else echo "FW none"; fi
  if [ -x $XT ]; then
    echo "XTUNE $($XT version 2>/dev/null | awk '{print $2}')"
    echo "XTUNE_SHA $(sha256sum $XT | cut -d' ' -f1)"
  else
    echo "XTUNE none"
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
xtune-link)
  chmod 755 $XT; link; install_timer
  # a new xtune binary: the subscription server runs it too
  systemctl try-restart xray-autotune-sub >/dev/null 2>&1 || true
  $XT version ;;
xtune-install)
  # the release build of xtune for this server, checksum-verified
  tag=$1 f=xtune_linux_$2.tar.gz
  b=https://github.com/chethober/xtune/releases/download/$tag
  t=$(mktemp -d); trap 'rm -rf "$t"' EXIT
  curl -fsSL -m 180 -o "$t/$f" "$b/$f"
  curl -fsSL -m 30 -o "$t/sums" "$b/checksums.txt"
  want=$(awk -v f="$f" '$2==f{print $1}' "$t/sums"); got=$(sha256sum "$t/$f" | cut -d' ' -f1)
  if [ -z "$want" ] || [ "$want" != "$got" ]; then echo "checksum mismatch ($got != $want)" >&2; exit 1; fi
  tar -xzf "$t/$f" -C "$t" xtune
  mkdir -p $D && chmod 700 $D
  install -m 755 "$t/xtune" $XT.new && mv -f $XT.new $XT
  link; install_timer
  $XT version ;;
probe)
  for h in "$@"; do
    echo "SNI $h $(curl -so /dev/null --tlsv1.3 --http2 -m 4 -w '%{http_version} %{time_appconnect}' "https://$h/" 2>/dev/null || echo fail)"
  done
  ss -Htuln | awk '{print "USED", $1, $5}'
  if command -v iptables >/dev/null && iptables -t nat -S PREROUTING >/dev/null 2>&1; then echo "HOP yes"; else echo "HOP no"; fi ;;
doctor)
  echo "TIME $(date +%s)"
  echo "NTP $(timedatectl show -p NTPSynchronized --value 2>/dev/null || echo unknown)"
  echo "HOPRULES $( (iptables -t nat -S PREROUTING 2>/dev/null || true) | grep -c 'comment xtune' || true)"
  ss -Htuln | awk '{print "USED", $1, $5}' ;;
hop-restore)
  hop_restore $D/fw.deployed ;;
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
check)
  if ! $X run -test -c $D/config.json >/dev/null 2>&1; then $X run -test -c $D/config.json 2>&1 | tail -5; exit 1; fi
  echo ok ;;
apply)
  if ! $X run -test -c $D/config.json >/dev/null 2>&1; then $X run -test -c $D/config.json 2>&1 | tail -5; exit 1; fi
  install_timer
  # written every time, so older deployments get the hop-rule restore after a reboot
  cat > $U/xray-autotune.service <<EOF
[Unit]
Description=Xray (managed by xtune)
After=network-online.target
Wants=network-online.target
[Service]
ExecStartPre=-/bin/bash $D/helper.sh hop-restore
ExecStart=$X run -c $D/config.json
Restart=on-failure
LimitNOFILE=65535
[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable xray-autotune >/dev/null 2>&1
  systemctl restart xray-autotune
  fw_close $D/fw.deployed; fw_open $D/fw.deployed "$@"
  sleep 1; systemctl is-active xray-autotune ;;
sub-enable)
  # the subscription server; arguments are the firewall port specs it needs
  cat > $U/xray-autotune-sub.service <<EOF
[Unit]
Description=xtune subscription server
After=network-online.target
Wants=network-online.target
[Service]
ExecStart=$XT serve
Restart=always
RestartSec=5
NoNewPrivileges=yes
PrivateTmp=yes
ProtectHome=yes
ProtectSystem=strict
ReadWritePaths=$D
[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable xray-autotune-sub >/dev/null 2>&1
  systemctl restart xray-autotune-sub
  fw_close $D/fw.sub; fw_open $D/fw.sub "$@"
  sleep 1; systemctl is-active xray-autotune-sub ;;
sub-disable)
  systemctl disable --now xray-autotune-sub >/dev/null 2>&1 || true
  rm -f $U/xray-autotune-sub.service; systemctl daemon-reload
  fw_close $D/fw.sub; echo disabled ;;
cleanup)
  # remove test leftovers; the whole dir goes only if nothing is deployed
  systemctl stop xray-autotune-test 2>/dev/null || true
  systemctl reset-failed xray-autotune-test 2>/dev/null || true
  fw_close $D/fw.test
  if [ -f /etc/systemd/system/xray-autotune.service ]; then rm -rf $D/test.json $D/test; echo kept-deployed
  else unlink_xt; rm -rf $D; echo removed; fi ;;
uninstall)
  remove_units
  fw_close $D/fw.test; fw_close $D/fw.deployed; fw_close $D/fw.sub
  unlink_xt; rm -rf $D; echo uninstalled ;;
*)
  echo "usage: helper.sh status|latest|install|xtune-link|xtune-install|probe|doctor|hop-restore|start|stop-test|check|apply|sub-enable|sub-disable|cleanup|uninstall" >&2; exit 2 ;;
esac
