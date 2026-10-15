# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses [Semantic Versioning](https://semver.org/).

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

[0.1.0]: https://github.com/chethober/xtune/releases/tag/v0.1.0
