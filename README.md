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
xtune tune    --host 203.0.113.10 --protocols all --dns-domain t.example.com   # + AmneziaWG, Worker, SSH/DNS tunnels, VPNs
XTUNE_CLOUDFLARE_TOKEN=... xtune tune --host 203.0.113.10 --zone example.com --protocols awg,worker
                                           # domains, DNS records and SSL mode made for you
xtune console --host 203.0.113.10          # the server's TUI, over SSH
xtune xray    --host 203.0.113.10 status   # local and server versions
xtune xray    --host 203.0.113.10 update
xtune logs    --host 203.0.113.10 -f
xtune check   --host 203.0.113.10 --network mci   # are the deployed configs still fine from here?
xtune check   --host 203.0.113.10 --every 6h      # keep checking, reporting to the server (rankings, autopilot)
xtune cfscan  --host 203.0.113.10                 # the Cloudflare edge IPs that get through from here
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
xtune users profiles carol --out carol/    # .ovpn, .sswan, .mobileconfig, WireGuard .conf, DNS tunnel details
xtune users enable|disable|rotate|reset-usage|reset-sub|del carol
xtune sub enable                           # public subscription links (through the CDN domain by default)
xtune sub lang fa                          # Persian pages; also ru, zh (default: the browser's language)
xtune sub metrics on                       # Prometheus metrics on the subscription server
xtune networks                             # configs ranked per network: subscriptions put each network's best first
xtune domains --direct vpn.example.com --cdn cdn.example.com
xtune domains --dns t.example.com          # the DNS tunnel's zone (NS record made with a Cloudflare token)
xtune country ir                           # Iranian sites go direct in the failover config
xtune notify telegram --chat 123456789     # alerts: quota, expiry, Xray down (asks for the bot token)
xtune backup schedule --telegram           # a sealed backup off the server every day (or --s3 https://ENDPOINT/BUCKET)
xtune autopilot digitalocean               # a blocked IP: alert, and on DigitalOcean move to a new reserved IP
xtune manage link                          # manage users from Telegram: send the link to @chthvpnbot
xtune plans add basic --quota 50 --days 30 # then: users add carol --plan basic, users renew carol
xtune public add community --quota 20 --days 30 --members 100   # share through the community VPN bot
xtune peers enable                         # on the primary; on other servers: xtune follow PEER_URL
xtune cloudflare token                     # migrate and domains then update the DNS records themselves
xtune cloudflare worker status|sync|off    # the Worker relay: its hostnames and routes
xtune cloudflare worker add                # another account's Worker on the same configs: another 100,000 requests a day
xtune warp on                              # AI services that refuse the server's IP go through WARP
```

SSH auth uses your agent or `~/.ssh` keys, `--key PATH`, or a password (prompted, or `--password-env VAR`).
Non-root users need sudo: you are prompted, or set `XTUNE_SUDO_PASSWORD`.

## What it does

| | |
|---|---|
| **Tune** | Starts a test matrix on free ports and tests each variant through a local Xray: reliability (fresh connections), latency, then sustained download/upload, which catches "fast for 3 seconds, then throttled". |
| **Test matrix** | VLESS + REALITY (TCP/Vision on the 3 fastest camouflage SNIs, a high port, XHTTP, gRPC), VLESS Encryption (post-quantum, TCP and XHTTP), Hysteria2 (a common UDP port such as 443, plus a high port), Shadowsocks-2022, WireGuard (a standard and a high UDP port, MTU 1420 and 1280) |
| **Other protocols** | `--protocols` adds, for the apps that speak them: **SSH tunnels** (xtune's own SSH server: plain, opening with a WebSocket upgrade as HTTP Injector payloads do, over TLS, and through the CDN domain), a **DNS tunnel** (dnstt, built in; `--dns-domain` names a zone delegated to the server, and each resolver is a variant: `--resolvers` you name, this network's, public UDP ones, DoH, DoT), **OpenVPN** (UDP and TCP) and **IKEv2** (built into phones and Windows). SSH and the DNS tunnel are measured like the Xray configs; for OpenVPN and IKEv2 only whether and how fast the handshake answers, since bringing a VPN up needs root. Deploy keeps the best working config of each protocol you asked for, besides the top N. |
| **AmneziaWG** | `--protocols awg`: WireGuard with its handshake disguised (junk packets, padded handshake messages, changed message types), for networks whose DPI blocks plain WireGuard. xtune serves it itself (amneziawg-go, on a standard and a high UDP port) and tunes it end to end from your machine, without root: MTU 1420 and 1280, more junk packets, and an I1 packet shaped like a QUIC Initial (AmneziaWG 1.5 apps). Users get a `.conf` file for the AmneziaVPN and AmneziaWG apps; traffic, quotas and online devices count like the other configs. |
| **Worker relay** | `--protocols worker` (needs a Cloudflare token): xtune creates a Cloudflare Worker that takes VLESS (with VLESS Encryption) over WebSocket on its `workers.dev` hostname, and on a random hostname in your zone when there is one, and relays it over TCP to the server. Clients never see the server's IP, so it keeps working when the IP is blocked, and needs no domain of your own. The Worker sends the client's address ahead of each connection (PROXY protocol), so device limits still see users, not Cloudflare. Tuning tries TLS fragmentation, which can get `workers.dev` past SNI filters. Workers' free plan allows 100,000 requests a day and limits CPU time, so long transfers may be cut on busy servers: hourly, the server reads each Worker's count (the token needs Account Analytics:Read) and alerts at 80% and 100%, and `xtune cloudflare worker add` adds a Worker in another account on the same configs, which subscriptions list next to the first (`limit N|none` for paid plans); Cloudflare's terms may not allow every proxy use, and the account is yours. |
| **Trojan** | `--protocols trojan`: Trojan over REALITY, on the REALITY keys and targets the VLESS configs use, for apps and networks that do better with Trojan (`trojan://` links). Skipped when the local Xray rejects it. |
| **Port hopping** | When the server has iptables, a Hysteria2 entry that listens on one port while a 1000-port UDP range is forwarded to it; the client keeps changing ports, which gets past per-port UDP blocking. Needs a recent Xray in the client (tested with 26.10.10); share links carry the range as `mport=`. |
| **Client knobs** | uTLS fingerprint (chrome, firefox, safari, edge, randomized), TLS ClientHello fragmentation, XHTTP mode, all tested per config. With a CDN domain, also a *split* CDN entry: uploads go through the CDN, downloads straight from the server. |
| **Clean IPs** | On many networks only part of Cloudflare's edge gets through, and which part differs by ISP. With a CDN domain or the Worker relay, tuning first scans a sample of Cloudflare's ranges from your machine (a TLS handshake for the domain, then `/cdn-cgi/trace`, which only a real edge answers) and also measures the CDN and Worker configs through the 3 best IPs (`--clean-ips N`, 0 = off): links then dial the IP, with the domain kept as SNI and Host. `xtune cfscan` lists the IPs that answer best from here. |
| **HTTP/3 to the CDN** | The CDN config is also tried over HTTP/3 (QUIC to Cloudflare; the CDN reaches the server over TCP as before), which some DPI does not look into. Needs a recent Xray in the client. |
| **Honest ranking** | Within 10% of the best score, the simplest settings win: fragmentation or exotic fingerprints are only recommended when they measurably help. |
| **Addresses** | Optional **direct domain** (DNS-only record for the server): links use it instead of the IP. Optional **CDN domain** (proxied record, e.g. Cloudflare orange cloud with SSL/TLS mode *Full*): adds a VLESS + XHTTP + TLS entry on a CDN port (2053, 2083, 2087, 2096, 8443, 443) that is tuned like every other config. |
| **Deploy** | Keeps the top N configs running as the `xray-autotune` systemd unit. Re-tuning keeps the server's keys and users, so only ports, SNIs and client settings change. |
| **Several networks** | `tune --share bundle.json` keeps the test running for 2 hours and writes a bundle for testers on other ISPs: `xtune measure bundle.json --network NAME` needs no SSH access and writes a report. `xtune deploy REPORT...` then deploys configs chosen so every network gets one that does well there. |
| **Per-network rankings** | Every tune, check and measured report knows its network's ASN (the client looks it up for itself) and stores how each deployed config did from there on the server. The subscription server looks up the ASN of whoever asks (Team Cymru's DNS service; through the CDN, the address Cloudflare saw) and lists that network's best configs first, with the clean IP that worked best there; configs blocked there go last, not away. `xtune networks` shows the rankings. |
| **Check** | `xtune check` re-tests only the deployed configs from where you are, in about a minute (`--every 6h` keeps doing it, from a phone in Termux or a computer on your users' network, and reports each check to the server for the rankings and the autopilot): blocked, flaky, slow, or throttled (fast at first, slow by the end). A hidden check user is used, so no user's traffic or devices are touched. Tunes and checks are kept in a local history (`xtune history`). |
| **Move servers** | `xtune backup` saves the server's keys, users and usage to a local file (`--encrypt` seals it with a passphrase). `xtune migrate --to NEWHOST` (or `--backup FILE` when the old server is gone) rebuilds the deployment on a new server, tuned from your network, with the same users, keys and subscription links: point the domains at the new IP (or let xtune do it, see *Cloudflare*) and users' apps follow on their next update. |
| **Blocked configs** | Xray counts traffic per deployed config. When one carries nothing for 2 hours while the others are in use, users' apps have quietly moved off it: it is probably blocked on their networks, and you get an alert (and another when it carries traffic again). The status in @chthvpnbot shows each config's last use. |
| **Scheduled backups** | `xtune backup schedule --telegram` (the alert chat) and/or `--s3 https://ENDPOINT/BUCKET[/PREFIX]` (Cloudflare R2, AWS S3, MinIO; keys in `XTUNE_S3_ACCESS_KEY`, `XTUNE_S3_SECRET_KEY`) sends a backup off the server every day (`--every`), sealed with a passphrase it prints once (or `XTUNE_BACKUP_PASSWORD`): keep it off the server. A failed backup is retried hourly and alerted once. Restore with `xtune migrate --backup FILE --to NEWHOST`. |
| **Autopilot** | When every config at the server's address goes quiet while the CDN or Worker configs are in use, or a recent check from some network finds them all blocked while a relayed one works, the IP is probably blocked: you get an alert with the `xtune migrate` command. `xtune autopilot digitalocean` (a DigitalOcean droplet, `XTUNE_DO_TOKEN`) moves the server instead, at most once a day: the droplet gets a new reserved IP (incoming connections need no setup on the droplet), the direct domain's record follows with a Cloudflare token, and subscriptions hand out the new address. |
| **REALITY targets** | Weekly, the server re-probes the sites its REALITY configs imitate. A target that stopped answering with TLS 1.3 + HTTP/2, or became slow, is replaced by the fastest working candidate when users get configs through subscriptions; otherwise you get an alert, and `xtune sni refresh` does it. |
| **Several servers** | `xtune peers enable` on one server (the primary) and `xtune follow PEER_URL` on others: followers take the primary's users and report their traffic to it every minute, and the primary's subscriptions and failover config list every server's configs. When one server's IP is blocked, users fail over to another. Quotas count traffic on all servers, and so do device limits: followers report who is online, and the primary counts each user's devices on every server and has followers block the newcomers. |
| **Cloudflare** | `xtune cloudflare token` saves an API token (Zone:Read, DNS:Edit, Zone Settings:Edit, and Account: Workers Scripts:Edit for the Worker relay); `tune` also takes one from `XTUNE_CLOUDFLARE_TOKEN` and keeps it on deploy. When tuning, new domains get their DNS records (DNS-only for the direct domain, proxied for the CDN domain, the NS delegation for the DNS tunnel) and the CDN domain's zone the SSL/TLS mode *Full*; with `--zone example.com`, domains you leave out get random names in that zone. Then `migrate` points the records (and the Worker) at the new server, `xtune domains` creates or updates them, `xtune cloudflare ssl` sets the SSL/TLS mode to *Full*, and `doctor` reads the mode from the API. The Cloudflare API is called from wherever the command runs: your machine for `tune`, the server for `xtune cloudflare`. |
| **Users** | Managed on the server only: add, list, enable/disable, rotate and delete, with share links (`vless://`, `hysteria2://`, `ss://`, `wireguard://`, `ssh://` for plain SSH) and profile files for the apps that have no links (OpenVPN, IKEv2, the WireGuard app, SSH and DNS tunnel apps such as HTTP Injector): `xtune users profiles` and the subscription page offer them. SSH and the VPNs log in with the user's name and a password derived from their keys (rotating the user changes it). Changes go live through Xray's API: nobody else is disconnected (with WireGuard deployed, Xray restarts, since its API cannot add WireGuard users). |
| **Limits** | Per-user traffic quota, expiry date and device limit (simultaneous client IPs), and optionally a monthly or weekly usage reset. A job runs every minute: it adds up traffic from Xray's stats (and the SSH tunnels' and VPNs' counters), records who is online, takes over-quota and expired users off (disconnecting their VPN sessions), and blocks extra devices for 10 minutes (SSH and OpenVPN refuse them at login). Traffic per day is kept for 60 days (`xtune users usage NAME`, a chart on the subscription page, the bot). |
| **Plans** | Named sets of limits: `xtune plans add basic --quota 50 --days 30 --devices 2 --reset monthly`, then `users add carol --plan basic`. `users renew carol` (or *Renew* in @chthvpnbot) extends the expiry by the plan's days from the current expiry, starts usage from zero and enables the user. |
| **Alerts** | Optional Telegram bot or webhook (Slack, Discord, ...): a user at 80% and 100% of their quota, 3 days before and at expiry, and the deployed Xray not answering. Each alert is sent once. |
| **Manage from Telegram** | `xtune manage link` prints a secret **management link** on the subscription server. Send it to [@chthvpnbot](https://t.me/chthvpnbot) (🛠 My servers → 🖥 Manage a server) to manage the server from your phone: status with each config's last use, users with their usage, adding them with limits or a plan, changing limits, renewing, enabling, disabling, resetting usage, new keys or a new subscription link, deleting, their links and QR codes, and plans. Nothing runs on the server for it and no port is opened beyond the subscription server. **Resellers**: `xtune manage reseller add rex --users 20 --quota 1000` prints a link of their own, which sees and changes only the users they add, within a user count and a total quota. `xtune manage token` (or `reseller token NAME`) makes a new link and the old one stops working; `xtune manage off` turns links off. Backups, keys, domains and the other settings stay on the command line. (Before v0.7.0 xtune ran its own Telegram bot; updating removes it.) |
| **Public profiles** | `xtune public add community --quota 20 --days 30 --devices 2 --members 100` (or `--plan basic`) makes a pool of users to share the server with a community, and prints a **connect link** on the subscription server. Send it to [@chthvpnbot](https://t.me/chthvpnbot), the open-source [community VPN bot](https://github.com/chethober/bots/tree/main/chthvpnbot), which lists the server publicly or behind a private invite (with an optional password). Everyone who joins gets **their own user** (`community-7f3a`) with the pool's limits and their own subscription link, so one member can be removed without touching the others, and quotas, expiry, device limits and peers work as for any user. The server never learns who a member is: the bot picks the labels and keeps Telegram identities to itself. `xtune public list` shows members per pool; `limit`, `pause`/`resume` (no new members), `token` (a new connect link, the old one stops working) and `del` (removes the members too). Members whose time ran out more than a week ago are removed, and a pool takes at most 30 new members a minute. The bot runs on tgcloud, which only reaches ports 80 and 443: when 443 is free, the subscription server also listens there and connect and management links use it (users' subscription links keep their port). |
| **Subscriptions** | `xtune sub enable` starts a small HTTPS server that gives every user one secret link: a page in the browser (usage, expiry, QR code, copy buttons), a subscription for apps (with the standard usage/expiry headers), and an automatic-failover config at `<link>/json`. It runs through the CDN domain (hides the server IP) or on the direct domain with a Let's Encrypt certificate. The page is in English, Persian (right-to-left, Solar Hijri dates), Russian or Chinese. `xtune sub metrics on` serves Prometheus metrics there too (users, traffic, each config's last use and whether it looks blocked, the enforce job, followers, backups, Worker requests), behind a token of their own. |
| **WARP** | `xtune warp on` registers a free Cloudflare WARP account on the server and sends chosen sites (by default AI services that refuse datacenter IPs) or all traffic out through it. The registration uses the API of Cloudflare's WARP apps, which is not a documented public API. |
| **Doctor** | `xtune doctor` checks services, scheduled backups, Worker requests, whether the IP looks blocked, that every deployed port listens, the direct domain's DNS, that the CDN domain is proxied and its SSL/TLS mode is *Full*, clock skew, and certificate expiry. |
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
- `- The deployed Xray has its API on a random `127.0.0.1` port, never reachable from outside.
- `xray-autotune-test`: a transient unit used while tuning. It stops on exit or Ctrl+C, and **expires after 2 hours** even if your machine disappears.
  With `tune --share`, it is left running for testers until then.
- For Hysteria2 port hopping: NAT rules (iptables/ip6tables, comment `xtune`) forwarding the UDP range to the entry's port.
  They are recorded, restored when Xray starts after a reboot, and removed on cleanup.
- `xray-autotune`: only if you deploy.
- With `--protocols ssh` or `dnstt`: `xray-autotune-tunnels`, xtune's SSH and DNS tunnel server (no system accounts;
  the DNS tunnel's UDP port gets port 53 forwarded to it with a NAT rule, like port hopping).
- With `--protocols awg`: `xray-autotune-awg`, xtune's AmneziaWG server, with a TUN interface per entry (`xtawg80`, ...)
  and, as for the VPNs below, IP forwarding and NAT rules for its clients' ranges (10.80.0.0/16 and up; 10.90.0.0/16
  and up while testing, removed when the test ends).
- With `--protocols worker`: nothing more on the server (a plain TCP port for each Worker config); the Worker itself
  lives in your Cloudflare account, and `xtune cloudflare worker off` deletes it.
- With `--protocols openvpn` or `ikev2`: the distribution's `openvpn` and strongSwan packages (`apt`/`dnf`),
  `xray-autotune-ovpn-*` units, `/etc/swanctl/conf.d/xtune.conf` with `xtune-ikev2` files in `/etc/swanctl/x509`
  and `/private`, IP forwarding (`/etc/sysctl.d/90-xtune-vpn.conf`) and iptables rules (comment `xtune-vpn`) that
  NAT the clients' ranges (10.68.0.0/16, 10.69.0.0/16) and keep them away from private addresses. Uninstalling
  removes all of that and turns strongSwan back off if xtune turned it on; the packages stay installed.
- Firewall rules (ufw/firewalld) only for ports it opens, and only if a firewall is active. They are removed again on cleanup.

Panels, nginx and other Xray installs are not modified; other strongSwan connections are left alone. Deployed configs block clients from reaching the
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
