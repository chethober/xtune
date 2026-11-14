# Operations: backups, moves, Cloudflare, upkeep

[Docs](README.md) · [Tuning](tuning.md) · [Server](server.md) · [Multi-hop](multihop.md) · [Operations](operations.md) · [Compatibility](compatibility.md) · [Architecture](architecture.md)

## Move servers

`xtune backup` saves the server's keys, users and usage to a local file (`--encrypt` seals it with a passphrase). `xtune migrate --to NEWHOST` (or `--backup FILE` when the old server is gone) rebuilds the deployment on a new server, tuned from your network, with the same users, keys and subscription links: point the domains at the new IP (or let xtune do it, see *Cloudflare*) and users' apps follow on their next update.

## Scheduled backups

`xtune backup schedule --telegram` (the alert chat) and/or `--s3 https://ENDPOINT/BUCKET[/PREFIX]` (Cloudflare R2, AWS S3, MinIO; keys in `XTUNE_S3_ACCESS_KEY`, `XTUNE_S3_SECRET_KEY`) sends a backup off the server every day (`--every`), sealed with a passphrase it prints once (or `XTUNE_BACKUP_PASSWORD`): keep it off the server. A failed backup is retried hourly and alerted once. Restore with `xtune migrate --backup FILE --to NEWHOST`.

## Autopilot

When every config at the server's address goes quiet while the CDN or Worker configs are in use, or a recent check from some network finds them all blocked while a relayed one works, the IP is probably blocked: you get an alert with the `xtune migrate` command. `xtune autopilot digitalocean` (a DigitalOcean droplet, `XTUNE_DO_TOKEN`) moves the server instead, at most once a day and only when a check confirms the block (quiet traffic alone can be a few users whose apps prefer the CDN config, so it only alerts and asks for a check): the droplet gets a new reserved IP (incoming connections need no setup on the droplet), the direct domain's record follows with a Cloudflare token, and subscriptions hand out the new address.

## Cloudflare

`xtune cloudflare token` saves an API token (Zone:Read, DNS:Edit, Zone Settings:Edit, and Account: Workers Scripts:Edit for the Worker relay, Account Analytics:Read for its usage alerts; [this link](https://dash.cloudflare.com/profile/api-tokens?permissionGroupKeys=%5B%7B%22key%22%3A%22zone%22%2C%22type%22%3A%22read%22%7D%2C%7B%22key%22%3A%22dns%22%2C%22type%22%3A%22edit%22%7D%2C%7B%22key%22%3A%22zone_settings%22%2C%22type%22%3A%22edit%22%7D%2C%7B%22key%22%3A%22workers_scripts%22%2C%22type%22%3A%22edit%22%7D%2C%7B%22key%22%3A%22workers_routes%22%2C%22type%22%3A%22edit%22%7D%2C%7B%22key%22%3A%22account_settings%22%2C%22type%22%3A%22read%22%7D%2C%7B%22key%22%3A%22account_analytics%22%2C%22type%22%3A%22read%22%7D%5D&name=xtune&accountId=%2A&zoneId=all) opens Cloudflare's token form with them filled in, and prints from `xtune cloudflare token`); `tune` also takes one from `XTUNE_CLOUDFLARE_TOKEN` and keeps it on deploy. When tuning, new domains get their DNS records (DNS-only for the direct domain, proxied for the CDN domain, the NS delegation for the DNS tunnel) and the CDN domain's zone the SSL/TLS mode *Full*; with `--zone example.com`, domains you leave out get random names in that zone. Then `migrate` points the records (and the Worker) at the new server, `xtune domains` creates or updates them, `xtune cloudflare ssl` sets the SSL/TLS mode to *Full*, and `doctor` reads the mode from the API. The Cloudflare API is called from wherever the command runs: your machine for `tune`, the server for `xtune cloudflare`.

## Doctor

`xtune doctor` checks services, scheduled backups, Worker requests, whether the IP looks blocked, that every deployed port listens, the direct domain's DNS, that the CDN domain is proxied and its SSL/TLS mode is *Full*, clock skew, and certificate expiry.

## Xray versions

Checks and updates Xray on your machine and on the server. Downloads are checksum-verified.

## Logs

Live, scrollable `journalctl` view for the deployed Xray or a running test.

## What xtune touches on the server

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
- On a relay: `xray-autotune-relay` and its config, and firewall rules for the ports it forwards.
- Firewall rules (ufw/firewalld) only for ports it opens, and only if a firewall is active. They are removed again on cleanup.

Panels, nginx and other Xray installs are not modified; other strongSwan connections are left alone. Deployed configs block clients from reaching the
server's private and localhost addresses. `xtune uninstall` removes everything listed above.

The server needs Linux (amd64 or arm64) with systemd, `bash` and `curl`. Your machine needs nothing; xtune downloads Xray if none is found
(including the one bundled with v2rayN).

