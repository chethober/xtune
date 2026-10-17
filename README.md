# xtune

[![ci](https://github.com/chethober/xtune/actions/workflows/ci.yml/badge.svg)](https://github.com/chethober/xtune/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/chethober/xtune?sort=semver)](https://github.com/chethober/xtune/releases)
[![license](https://img.shields.io/github/license/chethober/xtune)](LICENSE)

**Find the Xray configs that actually get through *your* network, deploy them, and manage users on the server, all from one binary.**

xtune connects to your VPS over SSH, starts a temporary matrix of [Xray](https://github.com/XTLS/Xray-core) inbounds
(REALITY, XHTTP, VLESS Encryption, Hysteria2, …), and measures every variant **from the machine you run it on**.
Run it from inside the censored network your users are on and it tells you what works there today, not what works in theory.

## Install

```sh
# Linux / macOS / Android (Termux)
curl -fsSL https://raw.githubusercontent.com/chethober/xtune/main/install.sh | sh
```

```powershell
# Windows (PowerShell)
irm https://raw.githubusercontent.com/chethober/xtune/main/install.ps1 | iex
```

Or `go install github.com/chethober/xtune@latest`, or grab a binary from [Releases](https://github.com/chethober/xtune/releases).
Both installers verify the release checksum before installing.

## Quick start

xtune is one binary with two TUIs:

- **On your computer** (the client): connect over SSH, tune from the network your users are on, deploy the winners.
- **On the server**: users and addresses. Deploying installs xtune on the server. Open it from the client's
  *Server console* menu (`xtune console`), or SSH in and run `sudo xtune`.

```sh
xtune            # on your computer: connect, tune, deploy, open the server console, view logs
sudo xtune       # on the server: users, subscriptions, addresses (direct / CDN domain), Xray, logs
```

The same features as plain commands:

```sh
# on your computer
xtune tune    --host 203.0.113.10 --deploy 3 --domain vpn.example.com --cdn cdn.example.com
xtune console --host 203.0.113.10          # the server's TUI, over SSH
xtune xray    --host 203.0.113.10 status   # local and server versions
xtune xray    --host 203.0.113.10 update
xtune logs    --host 203.0.113.10 -f
xtune check   --host 203.0.113.10 --network mci   # are the deployed configs still fine from here?
xtune doctor  --host 203.0.113.10          # services, ports, DNS, CDN SSL mode, clocks, certificates
xtune backup  --host 203.0.113.10          # keys, users and usage to a local file
xtune migrate --host 203.0.113.10 --to 198.51.100.20   # IP blocked: move everything to a new server
xtune uninstall --host 203.0.113.10
xtune self-update

# on the server (as root)
xtune users list                           # status, usage, quota, expiry, devices online (--json)
xtune users add carol --quota 50 --days 30 --devices 2 --reset monthly
xtune users limit carol --quota 100        # 0 = unlimited / never
xtune users links carol --qr               # QR code in the terminal (--json for scripts)
xtune users enable|disable|rotate|reset-usage|reset-sub|del carol
xtune sub enable                           # public subscription links (through the CDN domain by default)
xtune sub lang fa                          # Persian pages (default: the browser's language)
xtune domains --direct vpn.example.com --cdn cdn.example.com
xtune country ir                           # Iranian sites go direct in the failover config
xtune notify telegram --chat 123456789     # alerts: quota, expiry, Xray down (asks for the bot token)
xtune warp on                              # AI services that refuse the server's IP go through WARP
```

SSH auth uses your agent or `~/.ssh` keys, `--key PATH`, or a password (prompted, or `--password-env VAR`).
Non-root users need sudo: you are prompted, or set `XTUNE_SUDO_PASSWORD`.

## What it does

| | |
|---|---|
| **Tune** | Starts a test matrix on free ports and tests each variant through a local Xray: reliability (fresh connections), latency, then sustained download/upload, which catches "fast for 3 seconds, then throttled". |
| **Test matrix** | VLESS + REALITY (TCP/Vision on the 3 fastest camouflage SNIs, a high port, XHTTP, gRPC), VLESS Encryption (post-quantum, TCP and XHTTP), Hysteria2 (a common UDP port such as 443, plus a high port), Shadowsocks-2022 |
| **Port hopping** | When the server has iptables, a Hysteria2 entry that listens on one port while a 1000-port UDP range is forwarded to it; the client keeps changing ports, which gets past per-port UDP blocking. Needs a recent Xray in the client (tested with 26.10.10); share links carry the range as `mport=`. |
| **Client knobs** | uTLS fingerprint (chrome, firefox, safari, edge, randomized), TLS ClientHello fragmentation, XHTTP mode, all tested per config. With a CDN domain, also a *split* CDN entry: uploads go through the CDN, downloads straight from the server. |
| **Honest ranking** | Within 10% of the best score, the simplest settings win: fragmentation or exotic fingerprints are only recommended when they measurably help. |
| **Addresses** | Optional **direct domain** (DNS-only record for the server): links use it instead of the IP. Optional **CDN domain** (proxied record, e.g. Cloudflare orange cloud with SSL/TLS mode *Full*): adds a VLESS + XHTTP + TLS entry on a CDN port (2053, 2083, 2087, 2096, 8443, 443) that is tuned like every other config. |
| **Deploy** | Keeps the top N configs running as the `xray-autotune` systemd unit. Re-tuning keeps the server's keys and users, so only ports, SNIs and client settings change. |
| **Several networks** | `tune --share bundle.json` keeps the test running for 2 hours and writes a bundle for testers on other ISPs: `xtune measure bundle.json --network NAME` needs no SSH access and writes a report. `xtune deploy REPORT...` then deploys configs chosen so every network gets one that does well there. |
| **Check** | `xtune check` re-tests only the deployed configs from where you are, in about a minute: blocked, flaky, slow, or throttled (fast at first, slow by the end). A hidden check user is used, so no user's traffic or devices are touched. Tunes and checks are kept in a local history (`xtune history`). |
| **Move servers** | `xtune backup` saves the server's keys, users and usage to a local file. `xtune migrate --to NEWHOST` (or `--backup FILE` when the old server is gone) rebuilds the deployment on a new server, tuned from your network, with the same users, keys and subscription links: point the domains at the new IP and users' apps follow on their next update. |
| **Users** | Managed on the server only: add, list, enable/disable, rotate and delete, with share links (`vless://`, `hysteria2://`, `ss://`). Changes go live through Xray's API: nobody else is disconnected. |
| **Limits** | Per-user traffic quota, expiry date and device limit (simultaneous client IPs), and optionally a monthly or weekly usage reset. A job runs every minute: it adds up traffic from Xray's stats, records who is online, takes over-quota and expired users off, and blocks extra devices for 10 minutes. |
| **Alerts** | Optional Telegram bot or webhook (Slack, Discord, ...): a user at 80% and 100% of their quota, 3 days before and at expiry, and the deployed Xray not answering. Each alert is sent once. |
| **Subscriptions** | `xtune sub enable` starts a small HTTPS server that gives every user one secret link: a page in the browser (usage, expiry, QR code, copy buttons), a subscription for apps (with the standard usage/expiry headers), and an automatic-failover config at `<link>/json`. It runs through the CDN domain (hides the server IP) or on the direct domain with a Let's Encrypt certificate. The page is in English or Persian (right-to-left, Solar Hijri dates). |
| **WARP** | `xtune warp on` registers a free Cloudflare WARP account on the server and sends chosen sites (by default AI services that refuse datacenter IPs) or all traffic out through it. The registration uses the API of Cloudflare's WARP apps, which is not a documented public API. |
| **Doctor** | `xtune doctor` checks services, that every deployed port listens, the direct domain's DNS, that the CDN domain is proxied and its SSL/TLS mode is *Full*, clock skew, and certificate expiry. |
| **Failover config** | One Xray config per user with every deployed config behind a balancer: Xray health-checks them and uses the fastest working one, so a blocked config (or IP) fails over to the next, e.g. the CDN entry. Ads are blocked, and with `xtune country ir|cn|ru` that country's sites go direct. |
| **Xray versions** | Checks and updates Xray on your machine and on the server. Downloads are checksum-verified. |
| **Logs** | Live, scrollable `journalctl` view for the deployed Xray or a running test. |

Results land in `xtune-results/`: `results.csv`, `links.txt` and a ready-to-run `client_<config>.json` for each config.

## What it touches on the server

Everything lives in `/opt/xray-autotune` (root, mode 0700), including the server's copy of xtune, plus:

- `/usr/local/bin/xtune`: a link to that copy, so `sudo xtune` works. An existing xtune install there is left alone.
- `xray-autotune-enforce.timer`: the per-minute usage and limits job.
- `xray-autotune-sub`: the subscription server, only if you turn it on (its HTTPS port, plus port 80 in direct mode
  for Let's Encrypt).
- The deployed Xray has its API on a random `127.0.0.1` port, never reachable from outside.
- `xray-autotune-test`: a transient unit used while tuning. It stops on exit or Ctrl+C, and **expires after 2 hours** even if your machine disappears.
  With `tune --share`, it is left running for testers until then.
- For Hysteria2 port hopping: NAT rules (iptables/ip6tables, comment `xtune`) forwarding the UDP range to the entry's port.
  They are recorded, restored when Xray starts after a reboot, and removed on cleanup.
- `xray-autotune`: only if you deploy.
- Firewall rules (ufw/firewalld) only for ports it opens, and only if a firewall is active. They are removed again on cleanup.

Panels, nginx and other Xray installs are not modified. Deployed configs block clients from reaching the
server's private and localhost addresses. `xtune uninstall` removes everything listed above.

The server needs Linux (amd64 or arm64) with systemd, `bash` and `curl`. Your machine needs nothing; xtune downloads Xray if none is found
(including the one bundled with v2rayN).

## Build

```sh
git clone https://github.com/chethober/xtune && cd xtune
go build -trimpath -o xtune .
go test ./...
```

Releases are built by [GoReleaser](https://goreleaser.com) when a `v*` tag is pushed: desktop builds for Linux, macOS and
Windows (amd64/arm64), plus Android arm64 for Termux. The Linux builds are also what servers run.

## Responsible use

xtune is meant for running your own servers to keep access to the open internet. You are responsible for following the laws
that apply to you and your hosting provider's terms. See [SECURITY.md](SECURITY.md) to report vulnerabilities.

## License

[MIT](LICENSE)
