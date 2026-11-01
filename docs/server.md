# On the server: users, subscriptions, names

[Docs](README.md) · [Tuning](tuning.md) · [Server](server.md) · [Multi-hop](multihop.md) · [Operations](operations.md) · [Compatibility](compatibility.md) · [Architecture](architecture.md)

Users live on the server only. Manage them in the server TUI (`sudo xtune` on the server, or `xtune console`
from your computer), with `xtune users …` on the server, or from Telegram through [@chthvpnbot](https://t.me/chthvpnbot).

## Users

Managed on the server only: add, list, enable/disable, rotate and delete, with share links (`vless://`, `hysteria2://`, `ss://`, `wireguard://`, `ssh://` for plain SSH) and profile files for the apps that have no links (OpenVPN, IKEv2, the WireGuard app, SSH and DNS tunnel apps such as HTTP Injector): `xtune users profiles` and the subscription page offer them. SSH and the VPNs log in with the user's name and a password derived from their keys (rotating the user changes it). Changes go live through Xray's API: nobody else is disconnected (with WireGuard deployed, Xray restarts, since its API cannot add WireGuard users).

## Limits

Per-user traffic quota, expiry date and device limit (simultaneous client IPs), and optionally a monthly or weekly usage reset. A job runs every minute: it adds up traffic from Xray's stats (and the SSH tunnels' and VPNs' counters), records who is online, takes over-quota and expired users off (disconnecting their VPN sessions), and blocks extra devices for 10 minutes (SSH and OpenVPN refuse them at login). Traffic per day is kept for 60 days (`xtune users usage NAME`, a chart on the subscription page, the bot).

## Plans

Named sets of limits: `xtune plans add basic --quota 50 --days 30 --devices 2 --reset monthly`, then `users add carol --plan basic`. `users renew carol` (or *Renew* in @chthvpnbot) extends the expiry by the plan's days from the current expiry, starts usage from zero and enables the user.

## Subscriptions

`xtune sub enable` starts a small HTTPS server that gives every user one secret link: a page in the browser (usage, expiry, QR code, copy buttons), a subscription for apps (with the standard usage/expiry headers), and an automatic-failover config at `<link>/json`. It runs through the CDN domain (hides the server IP) or on the direct domain with a Let's Encrypt certificate. The page is in English, Persian (right-to-left, Solar Hijri dates), Russian or Chinese. `xtune sub metrics on` serves Prometheus metrics there too (users, traffic, each config's last use and whether it looks blocked, the enforce job, followers, backups, Worker requests), behind a token of their own.

## Names and country flags

Apps show each config by the name after `#` in its link, and a subscription by its `Profile-Title` header. xtune
puts the **flag of the country traffic leaves from** in front of both: the server's country, looked up once from its
IP (Team Cymru's DNS service; again after `xtune migrate`). Configs through a [relay](multihop.md) keep the main
server's flag, since that is still where traffic exits. Followers keep their own flags in the primary's subscriptions.

```sh
xtune names                                    # what configs and the subscription are called now
xtune names --label Frankfurt --country de     # {server} and the flag (auto = look it up, none = no flag)
xtune names --config "{flag} {server} · {proto} · {port}"
xtune names --sub "{flag} {server} VPN"        # the subscription's name in apps
xtune names --config= --sub=                   # back to the defaults
```

Templates take `{flag} {country} {server} {user} {id} {proto} {port} {via}` for configs (`{via}` is "via RELAY" on a
relay's configs) and `{flag} {country} {server} {user}` for the subscription. Parts left empty are dropped with their
separators. Without templates, names are what they were before 1.0 (`USER-ID`, `xtune - USER`) after the flag. The
server TUI has the same settings under *Names*. The registry country of an IP is usually where the server is; when it
is not, choose the country by hand.

## Named import links

The subscription page and `xtune users links NAME` also give one-tap links that add the subscription **under its
name** to v2rayNG (`v2rayng://install-sub`), Hiddify and Streisand (`…://import/URL#NAME`) and sing-box
(`sing-box://import-remote-profile`). Other apps take the plain subscription link.

## Failover config

One Xray config per user with every deployed config behind a balancer: Xray health-checks them and uses the fastest working one, so a blocked config (or IP) fails over to the next, e.g. the CDN entry. Ads are blocked, and with `xtune country ir|cn|ru` that country's sites go direct.

## Alerts

Optional Telegram bot or webhook (Slack, Discord, ...): a user at 80% and 100% of their quota, 3 days before and at expiry, and the deployed Xray not answering. Each alert is sent once.

## Manage from Telegram

`xtune manage link` prints a secret **management link** on the subscription server. Send it to [@chthvpnbot](https://t.me/chthvpnbot) (🛠 My servers → 🖥 Manage a server) to manage the server from your phone: status with each config's last use (and, for the admin, whether the IP looks blocked, scheduled backups and Worker requests), users with their usage, adding them with limits or a plan, changing limits, renewing, enabling, disabling, resetting usage, new keys or a new subscription link, deleting, their links and QR codes, and plans. Nothing runs on the server for it and no port is opened beyond the subscription server. **Resellers**: `xtune manage reseller add rex --users 20 --quota 1000` prints a link of their own, which sees and changes only the users they add, within a user count and a total quota. `xtune manage token` (or `reseller token NAME`) makes a new link and the old one stops working; `xtune manage off` turns links off. Backups, keys, domains and the other settings stay on the command line. (Before v0.7.0 xtune ran its own Telegram bot; updating removes it.)

## Public profiles

`xtune public add community --quota 20 --days 30 --devices 2 --members 100` (or `--plan basic`) makes a pool of users to share the server with a community, and prints a **connect link** on the subscription server. Send it to [@chthvpnbot](https://t.me/chthvpnbot), the open-source [community VPN bot](https://github.com/chethober/bots/tree/main/chthvpnbot), which lists the server publicly or behind a private invite (with an optional password). Everyone who joins gets **their own user** (`community-7f3a`) with the pool's limits and their own subscription link, so one member can be removed without touching the others, and quotas, expiry, device limits and peers work as for any user. The server never learns who a member is: the bot picks the labels and keeps Telegram identities to itself. `xtune public list` shows members per pool; `limit`, `pause`/`resume` (no new members), `token` (a new connect link, the old one stops working) and `del` (removes the members too). Members whose time ran out more than a week ago are removed, and a pool takes at most 30 new members a minute. The bot runs on tgcloud, which only reaches ports 80 and 443: when 443 is free, the subscription server also listens there and connect and management links use it (users' subscription links keep their port).

## WARP

`xtune warp on` registers a free Cloudflare WARP account on the server and sends chosen sites (by default AI services that refuse datacenter IPs) or all traffic out through it. The registration uses the API of Cloudflare's WARP apps, which is not a documented public API.

## Blocked configs

Xray counts traffic per deployed config. When one carries nothing for 2 hours while the others are in use, users' apps have quietly moved off it: it is probably blocked on their networks, and you get an alert (and another when it carries traffic again). The status in @chthvpnbot shows each config's last use.

## REALITY targets

Weekly, the server re-probes the sites its REALITY configs imitate. A target that stopped answering with TLS 1.3 + HTTP/2, or became slow, is replaced by the fastest working candidate when users get configs through subscriptions; otherwise you get an alert, and `xtune sni refresh` does it.

## Dashboard

A live, full-screen overview in both TUIs: each deployed config with its state (active, quiet/probably blocked, idle) and traffic, and the users with quota bars and who is online. Read-only and refreshing; `r` reloads, `q` goes back.