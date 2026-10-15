# xtune

[![ci](https://github.com/chethober/xtune/actions/workflows/ci.yml/badge.svg)](https://github.com/chethober/xtune/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/chethober/xtune?sort=semver)](https://github.com/chethober/xtune/releases)
[![license](https://img.shields.io/github/license/chethober/xtune)](LICENSE)

**Find the Xray configs that actually get through *your* network, deploy them, and manage users, all from one binary.**

xtune connects to your VPS over SSH, starts a temporary matrix of [Xray](https://github.com/XTLS/Xray-core) inbounds
(REALITY, XHTTP, VLESS Encryption, Hysteria2, …), and measures every variant **from the machine you run it on**.
Run it from inside the censored network your users are on and it tells you what works there today, not what works in theory.

## Install

```sh
# Linux / macOS / FreeBSD
curl -fsSL https://raw.githubusercontent.com/chethober/xtune/main/install.sh | sh
```

```powershell
# Windows (PowerShell)
irm https://raw.githubusercontent.com/chethober/xtune/main/install.ps1 | iex
```

Or `go install github.com/chethober/xtune@latest`, or grab a binary from [Releases](https://github.com/chethober/xtune/releases).
Both installers verify the release checksum before installing.

## Quick start

```sh
xtune            # interactive wizard: connect, tune, deploy, manage users, view logs
```

The same features as plain commands:

```sh
xtune tune  --host 203.0.113.10 --user root --deploy 3 --users alice,bob
xtune users --host 203.0.113.10 list
xtune users --host 203.0.113.10 add carol --quota 50 --days 30   # quota/expiry: 3x-ui backend
xtune users --host 203.0.113.10 links carol
xtune xray  --host 203.0.113.10 status                            # local, server and 3x-ui versions
xtune xray  --host 203.0.113.10 update
xtune logs  --host 203.0.113.10 -f
xtune uninstall --host 203.0.113.10
```

SSH auth uses your agent or `~/.ssh` keys, `--key PATH`, or a password (prompted, or `--password-env VAR`).
Non-root users need sudo: you are prompted, or set `XTUNE_SUDO_PASSWORD`.

## What it does

| | |
|---|---|
| **Tune** | Starts a test matrix on free ports and tests each variant through a local Xray: reliability (fresh connections), latency, then sustained download/upload, which catches "fast for 3 seconds, then throttled". |
| **Test matrix** | VLESS + REALITY (TCP/Vision on the 3 fastest camouflage SNIs, a high port, XHTTP, gRPC), VLESS Encryption (post-quantum, TCP and XHTTP), Hysteria2 (a common UDP port such as 443, plus a high port), Shadowsocks-2022 |
| **Client knobs** | uTLS fingerprint (chrome, firefox, safari, edge, randomized), TLS ClientHello fragmentation, XHTTP mode, all tested per config |
| **Honest ranking** | Within 10% of the best score, the simplest settings win: fragmentation or exotic fingerprints are only recommended when they measurably help. |
| **Deploy** | Keeps the top N configs running as the `xray-autotune` systemd unit and prints share links (`vless://`, `hysteria2://`, `ss://`) and a base64 subscription per user. |
| **Users** | Add, list, enable/disable, rotate and delete users for xtune's own deployment, **or** for an existing [3x-ui](https://github.com/MHSanaei/3x-ui) panel (with quotas, expiry and subscription links), through its API over SSH. The panel never needs to be exposed. |
| **Xray versions** | Checks and updates Xray on your machine, xtune's server copy and 3x-ui's copy. Downloads are checksum-verified. |
| **Logs** | Live, scrollable `journalctl` view for xtune's Xray, the 3x-ui panel or a running test. |

Results land in `xtune-results/`: `results.csv`, `links.txt` and a ready-to-run `client_<config>.json` for each config.

## What it touches on the server

Everything lives in `/opt/xray-autotune` (root, mode 0700), plus:

- `xray-autotune-test`: a transient unit used while tuning. It stops on exit or Ctrl+C, and **expires after 2 hours** even if your machine disappears.
- `xray-autotune`: only if you deploy.
- Firewall rules (ufw/firewalld) only for ports it opens, and only if a firewall is active. They are removed again on cleanup.

Existing panels, nginx and other Xray installs are not modified. Deployed configs block clients from reaching the
server's private and localhost addresses. `xtune uninstall` removes everything listed above.

The server needs Linux with systemd, `bash` and `curl`. Your machine needs nothing; xtune downloads Xray if none is found
(including the one bundled with v2rayN).

## Build

```sh
git clone https://github.com/chethober/xtune && cd xtune
go build -trimpath -o xtune .
go test ./...
```

Releases are built by [GoReleaser](https://goreleaser.com) for Linux, macOS, Windows and FreeBSD (amd64/arm64) when a `v*` tag is pushed.

## Responsible use

xtune is meant for running your own servers to keep access to the open internet. You are responsible for following the laws
that apply to you and your hosting provider's terms. See [SECURITY.md](SECURITY.md) to report vulnerabilities.

## License

[MIT](LICENSE)
