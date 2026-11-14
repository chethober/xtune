# Tuning: finding what gets through

[Docs](README.md) · [Tuning](tuning.md) · [Server](server.md) · [Multi-hop](multihop.md) · [Operations](operations.md) · [Compatibility](compatibility.md) · [Architecture](architecture.md)

`xtune tune` runs on your computer, connected to your server over SSH. Run it from the network your users are on:
it measures every config **from there**, so it tells you what works on that network today.

```sh
xtune tune --host 203.0.113.10 --deploy 3 --domain vpn.example.com --cdn cdn.example.com
xtune tune --host 203.0.113.10 --protocols all --dns-domain t.example.com
```

## Tune

Starts a test matrix on free ports and tests each variant through a local Xray: reliability (fresh connections), latency, then sustained download/upload, which catches "fast for 3 seconds, then throttled".

## Test matrix

VLESS + REALITY (TCP/Vision on the 3 fastest camouflage SNIs, a high port, XHTTP, gRPC), VLESS Encryption (post-quantum, TCP and XHTTP), Hysteria2 (a common UDP port such as 443, plus a high port), Shadowsocks-2022, WireGuard (a standard and a high UDP port, MTU 1420 and 1280)

## Other protocols

`--protocols` adds, for the apps that speak them: **SSH tunnels** (xtune's own SSH server: plain, opening with a WebSocket upgrade as HTTP Injector payloads do, over TLS, and through the CDN domain), a **DNS tunnel** (dnstt, built in; `--dns-domain` names a zone delegated to the server, and each resolver is a variant: `--resolvers` you name, this network's, public UDP ones, DoH, DoT), **OpenVPN** (UDP and TCP) and **IKEv2** (built into phones and Windows). SSH and the DNS tunnel are measured like the Xray configs; for OpenVPN and IKEv2 only whether and how fast the handshake answers, since bringing a VPN up needs root. Deploy keeps the best working config of each protocol you asked for, besides the top N.

## AmneziaWG

`--protocols awg`: WireGuard with its handshake disguised (junk packets, padded handshake messages, changed message types), for networks whose DPI blocks plain WireGuard. xtune serves it itself (amneziawg-go, on a standard and a high UDP port) and tunes it end to end from your machine, without root: MTU 1420 and 1280, more junk packets, and an I1 packet shaped like a QUIC Initial (AmneziaWG 1.5 apps). Users get a `.conf` file for the AmneziaVPN and AmneziaWG apps; traffic, quotas and online devices count like the other configs.

## Worker relay

`--protocols worker` (needs a Cloudflare token): xtune creates a Cloudflare Worker that takes VLESS (with VLESS Encryption) over WebSocket on its `workers.dev` hostname, and on a random hostname in your zone when there is one, and relays it over TCP to the server. Clients never see the server's IP, so it keeps working when the IP is blocked, and needs no domain of your own. The Worker sends the client's address ahead of each connection (PROXY protocol), so device limits still see users, not Cloudflare. Tuning tries TLS fragmentation, which can get `workers.dev` past SNI filters. Workers' free plan allows 100,000 requests a day and limits CPU time, so long transfers may be cut on busy servers: hourly, the server reads each Worker's count (the token needs Account Analytics:Read) and alerts at 80% and 100%, and `xtune cloudflare worker add` adds a Worker in another account on the same configs, which subscriptions list next to the first (`limit N|none` for paid plans); Cloudflare's terms may not allow every proxy use, and the account is yours.

## Trojan

`--protocols trojan`: Trojan over REALITY, on the REALITY keys and targets the VLESS configs use, for apps and networks that do better with Trojan (`trojan://` links). Skipped when the local Xray rejects it.

## MTProto proxy

`--protocols mtproto`: Telegram's own proxy protocol, for users who only need Telegram. xtune serves it in the tunnel server (a standard and a high TCP port) and relays to Telegram's data centres; each user gets their own secret, so quotas, expiry, device limits and disabling apply. It opens in the Telegram app from a `t.me/proxy` link or QR code, with nothing to install. Tuning measures the handshake and the path to a data centre, like the VPNs, since the proxy only reaches Telegram.

## Port hopping

When the server has iptables, a Hysteria2 entry that listens on one port while a 1000-port UDP range is forwarded to it; the client keeps changing ports, which gets past per-port UDP blocking. Needs a recent Xray in the client (tested with 26.10.10); share links carry the range as `mport=`.

## Client knobs

uTLS fingerprint (chrome, firefox, safari, edge, randomized), TLS ClientHello fragmentation, XHTTP mode, all tested per config. With a CDN domain, also a *split* CDN entry: uploads go through the CDN, downloads straight from the server.

## Clean IPs

On many networks only part of Cloudflare's edge gets through, and which part differs by ISP. With a CDN domain or the Worker relay, tuning first scans a sample of Cloudflare's ranges from your machine (a TLS handshake for the domain, then `/cdn-cgi/trace`, which only a real edge answers) and also measures the CDN and Worker configs through the 3 best IPs (`--clean-ips N`, 0 = off). The deployed configs keep the domain, which follows Cloudflare; a clean IP that beat it reaches that network's users through the per-network rankings, as links that dial the IP with the domain kept as SNI and Host. `xtune cfscan` lists the IPs that answer best from here.

## HTTP/3 to the CDN

The CDN config is also tried over HTTP/3 (QUIC to Cloudflare; the CDN reaches the server over TCP as before), which some DPI does not look into. Needs a recent Xray in the client.

## Honest ranking

Within 10% of the best score, the simplest settings win: fragmentation or exotic fingerprints are only recommended when they measurably help.

## Addresses

Optional **direct domain** (DNS-only record for the server): links use it instead of the IP. Optional **CDN domain** (proxied record, e.g. Cloudflare orange cloud with SSL/TLS mode *Full*): adds a VLESS + XHTTP + TLS entry on a CDN port (2053, 2083, 2087, 2096, 8443, 443) that is tuned like every other config.

## Deploy

Keeps the top N configs running as the `xray-autotune` systemd unit. Re-tuning keeps the server's keys and users, so only ports, SNIs and client settings change.

## Several networks

`tune --share bundle.json` keeps the test running for 2 hours and writes a bundle for testers on other ISPs: `xtune measure bundle.json --network NAME` needs no SSH access and writes a report. `xtune deploy REPORT...` then deploys configs chosen so every network gets one that does well there.

## Per-network rankings

Every tune, check and measured report knows its network's ASN (the client looks it up for itself) and stores how each deployed config did from there on the server. The subscription server looks up the ASN of whoever asks (Team Cymru's DNS service; through the CDN, the address Cloudflare saw) and lists that network's best configs first, with the clean IP that worked best there; configs blocked there go last, not away. `xtune networks` shows the rankings.

## Check

`xtune check` re-tests only the deployed configs from where you are, in about a minute (`--every 6h` keeps doing it, from a phone in Termux or a computer on your users' network, and reports each check to the server for the rankings and the autopilot): blocked, flaky, slow, or throttled (fast at first, slow by the end). A hidden check user is used, so no user's traffic or devices are touched. Tunes and checks are kept in a local history (`xtune history`).

## Results

Results land in `xtune-results/`: `results.csv`, `links.txt` and a ready-to-run `client_<config>.json` for each config.
