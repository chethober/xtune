# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses [Semantic Versioning](https://semver.org/).

## [0.7.0] - 2026-10-24

### Removed
- **Breaking:** xtune's own Telegram bot (`xtune bot`, the `xray-autotune-bot` unit), with its resellers by Telegram ID,
  user self-service and scheduled Telegram backups. The first enforce run after updating stops and removes the unit
  and drops the bot's settings and users' linked Telegram IDs; users added by resellers stay, managed by the admin.
  `xtune bot` now says where management went. Alerts (`xtune notify`) are unchanged, except that they no longer
  fall back to the bot.

### Added
- Management links (`xtune manage link|token|off`): users and plans from [@chthvpnbot](https://t.me/chthvpnbot) through
  `POST <link>` with `{"op": "status" | "users" | "user" | "add" | "limit" | "renew" | "enable" | "disable" |
  "resetUsage" | "rotate" | "resetSub" | "delete" | "plans" | "setPlan" | "delPlan", ...}` on the subscription server.
  Nothing runs on the server for it.
- Resellers as links (`xtune manage reseller add|limit|link|token|del NAME --users N --quota GB`): a reseller's link
  sees and changes only the users it added, within a user count and a total quota.
- The server TUI's *Telegram bot* screen is now *Manage from Telegram*.

### Fixed
- Re-tuning and deploying kept the users and settings but dropped the public profiles (`xtune public`).

## [0.6.0] - 2026-10-24

### Added
- Public profiles (`xtune public`): pools of users for sharing a server with a community through the open-source
  community VPN bot [@chthvpnbot](https://t.me/chthvpnbot) (open source: github.com/chethober/bots, `chthvpnbot`). `xtune public add NAME` takes per-member limits (or a
  plan) and a member cap, and prints a connect link on the subscription server. The bot joins and removes members
  through it (`POST <link>/join`, `POST <link>/leave` with `{"label": "..."}`, `GET <link>` for the pool's members,
  slots and protocols). Each member is an ordinary user `NAME-LABEL` with their own subscription link. Also
  `public list|limit|link|token|pause|resume|del`. Members expired for over a week are removed; joins are capped at
  30 a minute per pool.

## [0.5.1] - 2026-10-23

### Fixed
- `tune` with WireGuard and any other Xray protocol: the server test instance failed to start with
  `common/uuid: invalid UUID: <uuid>/mtu1280`. The MTU 1280 test peer, which exists only to get a WireGuard key of
  its own, was also added to the VLESS, Hysteria and Shadowsocks inbounds; it now goes on WireGuard inbounds only.

## [0.5.0] - 2026-10-23

### Fixed
- Android (Termux): downloads and every other lookup failed with `lookup github.com on [::1]:53: connection refused`
  (#4). Without cgo, Go reads nameservers only from `/etc/resolv.conf`, which Android does not have; xtune now asks
  `XTUNE_DNS` (comma-separated), Termux's own resolv.conf, then public resolvers, moving on when one does not answer.

### Added
- WireGuard, served by Xray: part of the default test matrix (a high port and a standard UDP port, MTU 1420 and 1280),
  with per-user keys and addresses, traffic accounting, quotas, `wireguard://` links and a `.conf` file for the
  official apps. Xray cannot add WireGuard users through its API, so user changes restart Xray when WireGuard is
  deployed.
- `tune --protocols ssh,dnstt,openvpn,ikev2` (or `vpn`, `all`) adds opt-in protocols to the test; a re-tune keeps
  testing the ones that are deployed, and deploy keeps the best working config of each besides the top N.
  - SSH tunnel: xtune's own SSH server (`xray-autotune-tunnels`), TCP forwarding only, no system accounts. Plain,
    plain with a WebSocket upgrade request first (HTTP Injector style payloads are answered), over TLS ("SSH-SSL"),
    and through the CDN domain. Plain SSH gets `ssh://` links (host key pinned); every SSH config also comes as a
    settings file for HTTP Injector style apps (server, port, SNI, payload, login).
  - DNS tunnel (dnstt, embedded): `--dns-domain t.example.com` (or `xtune domains --dns`), delegated to the server
    with an NS record (made through the Cloudflare API when a token is set). Variants per resolver: `--resolvers`,
    this network's, public UDP resolvers, DoH and DoT. It carries the SSH tunnel, the layout SlowDNS apps expect.
  - OpenVPN (UDP and TCP) and IKEv2 (strongSwan, EAP-MSCHAPv2), installed from the distribution's packages, with a
    CA of the deployment's own. Users log in with their name and password; `.ovpn`, `.sswan` (strongSwan app) and
    `.mobileconfig` (iPhone, iPad, Mac) profiles. Client ranges are NATed and kept away from private addresses.
    Tuning measures only whether and how fast their handshake answers: bringing a VPN up needs root on the client.
- Traffic, online devices, quotas, expiry and disabling apply to the SSH tunnels and VPNs too: the enforce job reads
  their counters and disconnects users who may no longer connect. Device limits are enforced at login for SSH and
  OpenVPN.
- `xtune users profiles NAME [--out DIR]` writes a user's profile files; the subscription page offers them for
  download with the SSH/VPN login, and the bot sends them with the links.
- `doctor` checks the tunnel server, VPN forwarding and NAT, and the DNS tunnel's delegation.

## [0.4.0] - 2026-10-21

### Added
- Blocked-config alerts: Xray now counts traffic per deployed config, and a config that carries nothing for 2 hours
  while others are in use is reported (once, and again when it is back). The bot's `/status` shows each config's
  last use.
- Usage history: traffic per user per day, kept 60 days: `xtune users usage NAME`, a 30-day chart on the
  subscription page, the last 7 days in the bot.
- Plans (`xtune plans`): named limits for `users add --plan` and `/add NAME PLAN`. `xtune users renew NAME`
  (and the bot's *Renew*) extends the expiry by the plan's days, from the current expiry, starts usage from zero
  and enables the user.
- Encrypted backups: `xtune backup --encrypt` (scrypt + AES-256-GCM); `restore` and `migrate --backup` ask for the
  passphrase (or read `XTUNE_BACKUP_PASSWORD`). `xtune bot backup on --every 24` sends admins one on a schedule;
  `/backup` sends one now.
- Bot: Persian (`xtune bot lang fa`); resellers who manage only their own users within a user count and total
  quota (`xtune bot resellers`); user self-service (`xtune bot self-service on`): users link their Telegram with
  their subscription link or an invite link, see their usage and links, and are warned at 80% and 100% of their
  quota and before and at expiry.
- Several servers under one subscription: `xtune peers enable` on a primary, `xtune follow PEER_URL` on others.
  Followers take the primary's users and report their traffic to it every minute; the primary's subscriptions and
  failover config include every server's configs.
- Cloudflare API (`xtune cloudflare`): `migrate` and `domains` update the DNS records, `cloudflare ssl` sets the
  SSL/TLS mode to Full, and `doctor` reads it from the API.
- Weekly REALITY target check: failing or slow targets are replaced (with subscriptions) or reported;
  `xtune sni check|refresh` runs it now.
- Server TUI: plans, renew, usage per day, several servers, Cloudflare, REALITY targets, and the bot's
  resellers, self-service, backups and language.
- Telegram management bot (`xtune bot enable`, or *Telegram bot* in the server TUI): list users, add them with
  limits, change limits, enable/disable, reset usage, rotate keys, reset subscription links and delete users, and
  get links with QR codes, from Telegram. It runs as the `xray-autotune-bot` unit, long-polls Telegram (no open
  port) and answers only its admins, who pair by messaging the bot or are set with `--admins` /
  `xtune bot admins ID,ID`. Destructive actions ask for confirmation, and commands sent while the bot was down
  for more than 10 minutes are dropped. With no alerts set up, enabling it sends alerts to the admin.
- `xtune doctor` checks the bot; deploys, restores and `self-update` keep it running.

## [0.3.0] - 2026-10-17

### Added
- `xtune backup`, `xtune restore` and `xtune migrate --to NEWHOST`: move a deployment to a new server (when its IP is
  blocked, say) with the same server keys, users, usage and subscription links. `migrate` tunes the new server from
  your network first; `--no-tune` keeps the configs as they were; `--backup FILE` works when the old server is gone.
- `xtune check`: a quick re-test of only the deployed configs from the current network, flagging blocked, flaky, slow
  and throttled ones. It measures with a hidden check user. Tunes and checks are kept in a local history
  (`xtune history`).
- Tuning for several networks: `tune --share FILE` leaves the test running for 2 hours and writes a bundle; testers on
  other ISPs run `xtune measure FILE` (no SSH access) and send back a report; `xtune deploy REPORT...` deploys configs
  chosen so every network gets one that does well there.
- Usage resets: `users add|limit --reset monthly|weekly`; the enforce job starts usage from zero each period.
- Persian subscription pages (right-to-left, Solar Hijri dates, Persian digits): picked from the browser's
  language, `?lang=`, or `xtune sub lang en|fa|auto`.
- `users list --json`, `users links NAME --json` and `users links NAME --qr` (QR codes in the terminal).
- Admin alerts by Telegram bot or webhook (`xtune notify`): quota at 80% and 100%, expiry soon and reached, and the
  deployed Xray not answering for 3 minutes.
- Hysteria2 port hopping in the test matrix (`hy2-hop`), when the server has iptables: a UDP range forwarded to one
  port, with the client's `udphop` mask. Skipped when the local Xray is too old for it.
- A split CDN variant (`split=cdn-up`): XHTTP uploads through the CDN, downloads straight from the server, into the
  same inbound.
- `xtune warp on|off`: a Cloudflare WARP outbound for chosen sites (default: AI services) or all traffic.
- `xtune self-update` (on the server it updates the server copy and restarts the subscription server), and a
  warning when the server runs a different xtune than the client.
- `xtune doctor`: services, listening ports, port-hopping rules, DNS records, the CDN's SSL/TLS mode, clock skew and
  certificate expiry.
- Client TUI: check, doctor, backup / move, update xtune. Server TUI: WARP, alerts, doctor, backup, update xtune,
  page language and usage resets.

### Changed
- The `xray-autotune` unit file is rewritten on every deploy, so it restores port-hopping rules after a reboot.

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

[0.7.0]: https://github.com/chethober/xtune/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/chethober/xtune/compare/v0.5.1...v0.6.0
[0.5.1]: https://github.com/chethober/xtune/compare/v0.5.0...v0.5.1
[0.5.0]: https://github.com/chethober/xtune/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/chethober/xtune/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/chethober/xtune/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/chethober/xtune/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/chethober/xtune/releases/tag/v0.1.0
