# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses [Semantic Versioning](https://semver.org/).

## [0.2.0] - 2026-10-17

### Added
- Server-side TUI: deploying installs xtune on the server, and `sudo xtune` there manages users, addresses, Xray and
  logs. `xtune console` (or *Server console* in the client TUI) opens it over SSH.
- Direct and CDN domains (`--domain`, `--cdn`, or the TUI). Links use the direct domain instead of the IP. A CDN domain
  adds a VLESS + XHTTP + TLS entry on a CDN-proxied port, included in the tune matrix. Both can be changed later on
  the server (`xtune domains`).
- Android (arm64, Termux) release builds and installer support.
- Live user changes through Xray's API (`adu`/`rmu`): adding, disabling, rotating or deleting a user no longer
  restarts Xray or disconnects anyone else. Older deployments migrate with one restart on their next change.
- Per-user traffic quota, expiry and device limit (`users add|limit --quota --days --devices`), enforced by the
  every-minute `xray-autotune-enforce` timer from Xray's stats: traffic totals, online IPs, over-quota and expired
  users taken off live, extra devices blocked for 10 minutes. `users list` shows status, usage and who is online;
  `users reset-usage` starts a user's traffic from zero.
- Subscription server (`xtune sub`, or *Subscription server* in the server TUI): one secret HTTPS link per user with
  a status page (usage, expiry, QR code), an app subscription with `Subscription-Userinfo` headers, and an
  automatic-failover Xray config at `/json`. Served through the CDN domain or with Let's Encrypt on the direct domain.
  `users reset-sub` issues a new link.
- Failover client config: every deployed config behind a `leastPing` balancer with health checks, ads blocked, and
  optionally a country's sites direct (`xtune country ir|cn|ru`).

### Changed
- Users are managed on the server only: `xtune users` runs there, and `tune --users` is gone. Deployments no longer
  create a default `user1`.
- Re-tuning an existing deployment keeps its server keys, certificate and users, so links change only where ports,
  SNIs or client settings change.
- The server copy of xtune lives in `/opt/xray-autotune` with a `/usr/local/bin/xtune` link; `uninstall` removes both.
- `xtune logs`, `xtune xray` and `xtune uninstall` work on the server itself when run without `--host`.

### Removed
- 3x-ui integration (panel users, quotas, expiry, panel Xray updates, panel logs).
- FreeBSD release builds.

## [0.1.0] - 2026-10-15

### Added
- `xtune tune`: temporary server test matrix (REALITY TCP/XHTTP/gRPC on several SNIs and ports, VLESS Encryption,
  Hysteria2, Shadowsocks-2022), tested from the local machine with uTLS fingerprint, fragmentation and XHTTP-mode variants;
  two-phase screening and sustained throughput benchmark; CSV results, share links and client configs.
- Deployment of the top N configs as the `xray-autotune` systemd unit, with per-user links and base64 subscriptions.
- User management for xtune deployments and for 3x-ui panels (quota, expiry, enable/disable, links).
- Xray version check and checksum-verified updates for the local binary, xtune's server copy and 3x-ui.
- Interactive TUI wizard with saved server profiles and a live log viewer.
- Safety: interrupt-safe cleanup, self-expiring server test instance, private-address blocking in deployed configs.
- One-line installers for Linux, macOS, FreeBSD and Windows.

[0.2.0]: https://github.com/chethober/xtune/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/chethober/xtune/releases/tag/v0.1.0
