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
# VPN client ranges are NATed out of the server and kept away from private addresses. The ranges
# are recorded in $D/vpn.nets so the rules come back after a reboot and go on uninstall.
vpn_rules() {
  local op=$1 net=$2 ins=-I
  [ "$op" = -A ] || ins=$op
  iptables -t nat "$op" POSTROUTING -s "$net" ! -d "$net" -m comment --comment xtune-vpn -j MASQUERADE 2>/dev/null &&
  iptables -t mangle "$op" FORWARD -s "$net" -p tcp --tcp-flags SYN,RST SYN -m comment --comment xtune-vpn -j TCPMSS --clamp-mss-to-pmtu 2>/dev/null &&
  iptables "$ins" FORWARD -d "$net" -m conntrack --ctstate RELATED,ESTABLISHED -m comment --comment xtune-vpn -j ACCEPT 2>/dev/null &&
  iptables "$ins" FORWARD -s "$net" -m comment --comment xtune-vpn -j ACCEPT 2>/dev/null || return 1
  # inserted last, so they come first
  for p in 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 100.64.0.0/10 169.254.0.0/16 127.0.0.0/8; do
    iptables "$ins" FORWARD -s "$net" -d "$p" -m comment --comment xtune-vpn -j DROP 2>/dev/null || return 1
  done
}
vpn_net_up() {
  sysctl -qw net.ipv4.ip_forward=1
  echo 'net.ipv4.ip_forward = 1' > /etc/sysctl.d/90-xtune-vpn.conf
  for net in "$@"; do
    vpn_rules -C "$net" || vpn_rules -A "$net" || { echo "iptables failed for $net" >&2; return 1; }
    grep -qxF "$net" $D/vpn.nets 2>/dev/null || echo "$net" >> $D/vpn.nets
  done
}
vpn_net_down() {
  [ -f $D/vpn.nets ] || return 0
  while read -r net; do while vpn_rules -D "$net"; do :; done; done < $D/vpn.nets
  rm -f $D/vpn.nets /etc/sysctl.d/90-xtune-vpn.conf
}
vpn_net_restore() {
  [ -f $D/vpn.nets ] || return 0
  while read -r net; do vpn_rules -C "$net" || vpn_rules -A "$net" || true; done < $D/vpn.nets
}
swan_unit() {
  for u in strongswan strongswan-swanctl; do
    if systemctl cat "$u" >/dev/null 2>&1; then echo "$u"; return; fi
  done
  echo strongswan
}
SWANCONF=/etc/swanctl/conf.d/xtune.conf
# swan_install DIR puts xtune's strongSwan config, certificate and key where swanctl loads them
swan_install() {
  install -m 644 "$1/ikev2.crt" /etc/swanctl/x509/xtune-ikev2.crt
  install -m 600 "$1/ikev2.key" /etc/swanctl/private/xtune-ikev2.key
  install -m 600 "$1/swanctl.conf" $SWANCONF
}
swan_remove() { rm -f $SWANCONF /etc/swanctl/x509/xtune-ikev2.crt /etc/swanctl/private/xtune-ikev2.key; }
swan_load() { swanctl --load-creds --clear >/dev/null 2>&1 && swanctl --load-pools >/dev/null 2>&1 && swanctl --load-conns >/dev/null 2>&1; }
# unit_file NAME DESCRIPTION EXEC writes a deployed service that restarts on failure
unit_file() {
  cat > "$U/$1.service" <<EOF
[Unit]
Description=$2
After=network-online.target
Wants=network-online.target
[Service]
ExecStart=$3
Restart=always
RestartSec=3
LimitNOFILE=65535
[Install]
WantedBy=multi-user.target
EOF
}
# extras_stop stops the deployed tunnel and VPN services, except those named
extras_stop() {
  local keep=" $* " u
  for u in $(systemctl list-unit-files --no-legend 'xray-autotune-tunnels.service' 'xray-autotune-ovpn-*.service' 2>/dev/null | awk '{print $1}'); do
    case $keep in *" ${u%.service} "*) continue ;; esac
    systemctl disable --now "$u" >/dev/null 2>&1 || true
    rm -f "$U/$u"
  done
  case $keep in *" ikev2 "*) ;; *)
    if [ -f $SWANCONF ] && [ ! -f $D/test/ikev2-temp ]; then
      swan_remove
      if command -v swanctl >/dev/null; then
        swanctl --terminate --ike xtune-ikev2 --force >/dev/null 2>&1 || true
        swan_load || true
      fi
      # strongSwan goes back off if xtune turned it on
      if [ -f $D/swan-ours ]; then systemctl disable --now "$(swan_unit)" >/dev/null 2>&1 || true; rm -f $D/swan-ours; fi
    fi ;;
  esac
}
test_stop_extras() {
  for u in $(systemctl list-units --all --no-legend 'xray-autotune-test-*' 2>/dev/null | awk '{print $1}'); do
    systemctl stop "$u" 2>/dev/null || true
    systemctl reset-failed "$u" 2>/dev/null || true
  done
  if [ -f $D/test/ikev2-temp ]; then
    swan_remove
    swan_load 2>/dev/null || true
    if [ -f $D/test/swan-was-off ]; then systemctl stop "$(swan_unit)" 2>/dev/null || true; fi
  fi
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
  extras_stop; test_stop_extras
  systemctl disable --now xray-autotune xray-autotune-test xray-autotune-sub xray-autotune-bot xray-autotune-enforce.timer >/dev/null 2>&1 || true
  rm -f $U/xray-autotune.service $U/xray-autotune-sub.service $U/xray-autotune-bot.service $U/xray-autotune-enforce.service $U/xray-autotune-enforce.timer
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
  echo "TUNNELS $(systemctl is-active xray-autotune-tunnels 2>/dev/null || true)"
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
  if [ -f $SWANCONF ]; then echo "IKE ours"; fi
  if command -v iptables >/dev/null && iptables -t nat -S PREROUTING >/dev/null 2>&1; then echo "HOP yes"; else echo "HOP no"; fi ;;
doctor)
  echo "TIME $(date +%s)"
  echo "NTP $(timedatectl show -p NTPSynchronized --value 2>/dev/null || echo unknown)"
  echo "HOPRULES $( (iptables -t nat -S PREROUTING 2>/dev/null || true) | grep -c 'comment xtune' || true)"
  echo "VPNRULES $( (iptables -t nat -S POSTROUTING 2>/dev/null || true) | grep -c 'comment xtune-vpn' || true)"
  echo "FORWARD $(sysctl -n net.ipv4.ip_forward 2>/dev/null || echo 0)"
  ss -Htuln | awk '{print "USED", $1, $5}' ;;
hop-restore)
  hop_restore $D/fw.deployed; vpn_net_restore ;;
vpn-install)
  # OpenVPN and strongSwan (swanctl, EAP-MSCHAPv2) from the distribution
  need=""
  command -v openvpn >/dev/null || need=1
  command -v swanctl >/dev/null || need=1
  if [ -n "$need" ]; then
    if command -v apt-get >/dev/null; then
      export DEBIAN_FRONTEND=noninteractive
      apt-get update -qq >/dev/null
      # EAP-Identity is in the extra plugins; EAP-MSCHAPv2 moved to extauth (Debian 11+, Ubuntu 22.04+)
      apt-get install -y -qq openvpn strongswan-swanctl charon-systemd libcharon-extra-plugins >/dev/null
      apt-get install -y -qq libcharon-extauth-plugins >/dev/null 2>&1 || true
    elif command -v dnf >/dev/null; then
      dnf install -y -q epel-release >/dev/null 2>&1 || true
      dnf install -y -q openvpn strongswan >/dev/null
    else
      echo "no apt-get or dnf: install openvpn and strongswan (swanctl) yourself" >&2; exit 1
    fi
  fi
  # the distribution's own OpenVPN server units stay off; xtune runs its own
  command -v openvpn >/dev/null && command -v swanctl >/dev/null && echo ok ;;
test-extras)
  # test instances of the tunnel server and VPNs; they expire with the Xray test instance
  for spec in "$@"; do
    case $spec in
      tunnels)
        systemd-run --quiet --unit=xray-autotune-test-tunnels --property=RuntimeMaxSec=7200 $XT tunnels run $D/test/tunnels.json ;;
      openvpn:*)
        id=${spec#openvpn:}
        systemd-run --quiet --unit="xray-autotune-test-ovpn-$id" --property=RuntimeMaxSec=7200 \
          "$(command -v openvpn)" --cd $D/test/vpn --config "$D/test/vpn/$id.conf" ;;
      ikev2)
        su=$(swan_unit)
        if [ ! -f $SWANCONF ]; then
          systemctl is-active "$su" >/dev/null 2>&1 || touch $D/test/swan-was-off
          swan_install $D/test/vpn; touch $D/test/ikev2-temp
        fi
        systemctl start "$su"; sleep 1; swan_load ;;
    esac
  done
  sleep 1
  for spec in "$@"; do
    case $spec in
      tunnels) echo "tunnels $(systemctl is-active xray-autotune-test-tunnels 2>/dev/null || true)" ;;
      openvpn:*) echo "$spec $(systemctl is-active "xray-autotune-test-ovpn-${spec#openvpn:}" 2>/dev/null || true)" ;;
      ikev2) echo "ikev2 $(systemctl is-active "$(swan_unit)" 2>/dev/null || true)" ;;
    esac
  done ;;
extras)
  # the deployed tunnel server and VPNs: start those named, stop the others, NAT the VPN ranges
  keep="" nets=""
  for spec in "$@"; do
    case $spec in
      tunnels)
        unit_file xray-autotune-tunnels "xtune SSH and DNS tunnels" "$XT tunnels run $D/tunnels.json"
        keep="$keep xray-autotune-tunnels" ;;
      openvpn:*)
        id=${spec#openvpn:}
        unit_file "xray-autotune-ovpn-$id" "OpenVPN $id (managed by xtune)" "$(command -v openvpn) --cd $D/vpn --config $D/vpn/$id.conf"
        keep="$keep xray-autotune-ovpn-$id" ;;
      ikev2)
        swan_install $D/vpn; rm -f $D/test/ikev2-temp $D/test/swan-was-off
        keep="$keep ikev2" ;;
      net:*) nets="$nets ${spec#net:}" ;;
    esac
  done
  # shellcheck disable=SC2086
  extras_stop $keep
  vpn_net_down
  # shellcheck disable=SC2086
  if [ -n "$nets" ]; then vpn_net_up $nets; fi
  systemctl daemon-reload
  for u in $keep; do
    case $u in
      ikev2)
        su=$(swan_unit)
        if ! systemctl is-enabled "$su" >/dev/null 2>&1; then touch $D/swan-ours; fi
        systemctl enable --now "$su" >/dev/null 2>&1; sleep 1; swan_load ;;
      *) systemctl enable "$u" >/dev/null 2>&1; systemctl restart "$u" ;;
    esac
  done
  sleep 1
  for u in $keep; do
    case $u in
      ikev2) echo "ikev2 $(systemctl is-active "$(swan_unit)" 2>/dev/null || true)" ;;
      *) echo "$u $(systemctl is-active "$u" 2>/dev/null || true)" ;;
    esac
  done ;;
vpn-reload)
  # new user secrets for strongSwan; OpenVPN reads its users file on every login
  if [ -f $SWANCONF ] && [ -f $D/vpn/swanctl.conf ]; then install -m 600 $D/vpn/swanctl.conf $SWANCONF; swan_load; fi
  echo ok ;;
vpn-sessions)
  # what the enforce job counts: OpenVPN status files and strongSwan's SAs
  for f in "$D"/vpn/*.status; do [ -f "$f" ] && { echo "### ovpn $(basename "$f" .status)"; cat "$f"; }; done
  if [ -f $SWANCONF ] && command -v swanctl >/dev/null; then echo "### ike"; swanctl --list-sas --raw 2>/dev/null || true; fi ;;
vpn-kick)
  # vpn-kick ovpn:ID:USER | ike:UNIQUEID
  for k in "$@"; do
    case $k in
      ovpn:*) IFS=: read -r _ id user <<< "$k"
        $XT vpn-mgmt "$D/vpn/$id.sock" "kill $user" >/dev/null 2>&1 || true ;;
      ike:*) swanctl --terminate --ike-id "${k#ike:}" --force >/dev/null 2>&1 || true ;;
    esac
  done
  echo ok ;;
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
  test_stop_extras
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
bot-remove)
  # the Telegram bot of xtune before v0.7.0; management moved to the community VPN bot
  systemctl disable --now xray-autotune-bot >/dev/null 2>&1 || true
  rm -f $U/xray-autotune-bot.service; systemctl daemon-reload; echo disabled ;;
cleanup)
  # remove test leftovers; the whole dir goes only if nothing is deployed
  systemctl stop xray-autotune-test 2>/dev/null || true
  systemctl reset-failed xray-autotune-test 2>/dev/null || true
  test_stop_extras
  fw_close $D/fw.test
  if [ -f /etc/systemd/system/xray-autotune.service ]; then rm -rf $D/test.json $D/test; echo kept-deployed
  else unlink_xt; rm -rf $D; echo removed; fi ;;
uninstall)
  remove_units
  fw_close $D/fw.test; fw_close $D/fw.deployed; fw_close $D/fw.sub; vpn_net_down
  unlink_xt; rm -rf $D; echo uninstalled ;;
*)
  echo "usage: helper.sh status|latest|install|xtune-link|xtune-install|probe|doctor|hop-restore|vpn-install|test-extras|extras|vpn-reload|vpn-sessions|vpn-kick|start|stop-test|check|apply|sub-enable|sub-disable|bot-remove|cleanup|uninstall" >&2; exit 2 ;;
esac
