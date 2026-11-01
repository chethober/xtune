# xtune

[![ci](https://github.com/chethober/xtune/actions/workflows/ci.yml/badge.svg)](https://github.com/chethober/xtune/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/chethober/xtune?sort=semver)](https://github.com/chethober/xtune/releases)
[![license](https://img.shields.io/github/license/chethober/xtune)](LICENSE)

**Find the Xray configs that actually get through *your* network, deploy them, and manage users on the server, all from one binary.**

xtune connects to your VPS over SSH, starts a temporary matrix of [Xray](https://github.com/XTLS/Xray-core) inbounds
(REALITY, XHTTP, VLESS Encryption, Hysteria2, WireGuard, …), plus, if you want them, AmneziaWG, a Cloudflare Worker
relay, SSH and DNS tunnels, OpenVPN and IKEv2, and measures every variant **from the machine you run it on**.
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
On Android, xtune looks names up through Termux's resolver settings, then public resolvers; set `XTUNE_DNS=IP[,IP]`
to choose them yourself.

## Quick start

xtune is one binary with two TUIs:

- **On your computer** (the client): connect over SSH, tune from the network your users are on, deploy the winners.
- **On the server**: users, subscriptions and addresses. Deploying installs xtune there. Open it from the client's
  *Server console* menu (`xtune console`), or SSH in and run `sudo xtune`.

```sh
xtune            # on your computer: connect, tune, deploy, relays, open the server console, view logs
sudo xtune       # on the server: users, subscriptions, names, addresses, Xray, logs
```

The same as plain commands:

```sh
# on your computer
xtune tune    --host 203.0.113.10 --deploy 3 --domain vpn.example.com --cdn cdn.example.com
xtune tune    --host 203.0.113.10 --protocols all --dns-domain t.example.com   # + AmneziaWG, Worker, SSH/DNS tunnels, MTProto, VPNs
xtune relay   add teh1 --host 203.0.113.10 --ssh 198.51.100.20   # multi-hop through a server in your users' country
xtune check   --host 203.0.113.10 --every 6h      # keep checking from here, reporting to the server
xtune doctor  --host 203.0.113.10                 # services, ports, DNS, CDN SSL mode, clocks, certificates, relays
xtune migrate --host 203.0.113.10 --to 198.51.100.30   # IP blocked: move everything to a new server

# on the server (as root)
xtune users add carol --quota 50 --days 30 --devices 2 --reset monthly
xtune users links carol --qr                  # QR code in the terminal (--json for scripts)
xtune sub enable                              # one link per user: a page, a subscription, a failover config
xtune names --label Frankfurt --config "{flag} {server} · {proto}"   # how configs are named in apps
xtune manage link                             # manage users from Telegram: send the link to @chthvpnbot
```

SSH auth uses your agent or `~/.ssh` keys, `--key PATH`, or a password (prompted, or `--password-env VAR`).
Non-root users need sudo: you are prompted, or set `XTUNE_SUDO_PASSWORD`. `xtune help` lists every command.

## What it does

- **Measures, from where you are.** A temporary matrix of VLESS + REALITY (TCP/Vision, XHTTP, gRPC), VLESS
  Encryption, Hysteria2 (with port hopping), Shadowsocks-2022 and WireGuard, and on request AmneziaWG, Trojan, a
  Cloudflare Worker relay, SSH and DNS tunnels, MTProto, OpenVPN and IKEv2, each with its client knobs (uTLS
  fingerprint, TLS fragmentation, XHTTP mode, MTU), tested for reliability, latency and *sustained* throughput, which
  catches "fast for 3 seconds, then throttled". With a CDN domain it also finds the Cloudflare edge IPs that get
  through from your network. [Tuning →](docs/tuning.md)
- **Deploys the winners** as one systemd unit, and keeps the server's keys and users across re-tunes, so links keep
  working.
- **Manages users on the server**: quotas, expiry, device limits, monthly resets, plans, live changes through Xray's
  API, and a usage job every minute.
- **One link per user**: a page (English, Persian, Russian, Chinese), a subscription for apps with usage headers, and
  a failover config that switches to the next working config when one is blocked. Subscriptions list each network's
  best configs first. [Server →](docs/server.md)
- **Names configs with the exit country's flag** (🇩🇪 `bob-r-tcp`), or your own template (`{flag} {server} · {proto}`),
  and gives one-tap import links that add the subscription to v2rayNG, Hiddify, Streisand and sing-box under its
  name. [Names →](docs/server.md#names-and-country-flags)
- **Multi-hop**: a relay in your users' country forwards to the main server, holding no keys; its configs join the
  subscriptions. Several servers can share one subscription too. [Multi-hop →](docs/multihop.md)
- **Keeps it running**: alerts when a config goes quiet (probably blocked), REALITY targets re-checked weekly,
  scheduled sealed backups to Telegram or S3, `migrate` to a new server with the same links, an autopilot that moves
  a blocked DigitalOcean droplet to a new IP, Cloudflare DNS and SSL handled with a token, `doctor`.
  [Operations →](docs/operations.md)
- **Manage from your phone** through [@chthvpnbot](https://t.me/chthvpnbot), with resellers and public pools shared
  with a community.

## Stable from 1.0

`state.json`, backups, commands and flags, `--json` output and users' links stay compatible across 1.x; files from
every 0.x release are migrated, and files from a newer xtune are never overwritten. See
[Compatibility](docs/compatibility.md).

## What it touches on the server

Everything lives in `/opt/xray-autotune` (root, mode 0700), plus the `xray-autotune*` systemd units for what you turn
on, the `/usr/local/bin/xtune` link, and firewall and NAT rules it records and removes itself. Panels, nginx and other
Xray installs are left alone, and `xtune uninstall` removes everything. The full list is in
[Operations](docs/operations.md#what-xtune-touches-on-the-server).

The server needs Linux (amd64 or arm64) with systemd, `bash` and `curl`. Your machine needs nothing; xtune downloads
Xray if none is found (including the one bundled with v2rayN).

## Build

```sh
git clone https://github.com/chethober/xtune && cd xtune
go build -trimpath -o xtune .
go test ./...
```

See [CONTRIBUTING.md](CONTRIBUTING.md) and [Architecture](docs/architecture.md). Releases are built by
[GoReleaser](https://goreleaser.com) when a `v*` tag is pushed: Linux, macOS and Windows (amd64/arm64), plus Android
arm64 for Termux. The Linux builds are also what servers run.

## Responsible use

xtune is meant for running your own servers to keep access to the open internet. You are responsible for following the laws
that apply to you and your hosting provider's terms. See [SECURITY.md](SECURITY.md) to report vulnerabilities.

## License

[MIT](LICENSE)
