# Multi-hop and several servers

[Docs](README.md) · [Tuning](tuning.md) · [Server](server.md) · [Multi-hop](multihop.md) · [Operations](operations.md) · [Compatibility](compatibility.md) · [Architecture](architecture.md)

## Relays (multi-hop)

On some networks, routes inside the country are filtered less than international ones, and datacenter-to-datacenter
links less than home and mobile ones. A **relay** is a server in your users' own country that forwards ports to your
main server: client → relay → server.

```sh
xtune relay add teh1 --host 203.0.113.10 --ssh 198.51.100.20     # set up the relay, measure, list its configs
xtune relay --host 203.0.113.10                                  # relays, their configs and latency
xtune relay sync teh1 --host 203.0.113.10                        # after a redeploy or a move
xtune relay del teh1 --host 203.0.113.10                         # stop it there and forget it (--forget if unreachable)
```

`--ssh-user`, `--ssh-port`, `--ssh-key` and `--ssh-password-env` reach the relay; `--addr` is the address clients
dial if not its SSH host (a domain, say); `--only r-tcp,hy2` forwards only those configs. The client TUI has the
same under *Relays*.

- **Nothing secret on the relay.** It runs only Xray as a port forwarder (unit `xray-autotune-relay`, files in
  `/opt/xray-autotune`): no keys, no users. REALITY, Hysteria2, WireGuard and the SSH tunnels still end at the main
  server, so someone with the relay sees encrypted traffic and nothing to decrypt it with.
- **Measured from the relay.** Each TCP port is measured from the relay to the main server, and ports the relay
  cannot reach are left out. UDP (Hysteria2, WireGuard) cannot be measured this way and is forwarded as is.
- **Ports.** The relay listens on each config's own port when it is free there, otherwise on a free high port.
- **In every subscription.** The relay's configs appear after the main server's, named `RELAY-ID` (`teh1-r-tcp`)
  after the relay's flag and the main server's (`🇮🇷→🇳🇱`; the relay's country is looked up from the relay on
  `add` and `sync`), and in the failover config, so apps fail over between direct and relayed paths.
- **What can go through:** REALITY, VLESS Encryption, Trojan, Hysteria2 (without port hopping), Shadowsocks,
  WireGuard, the SSH tunnels and MTProto. CDN and Worker configs already have their own path; the DNS tunnel goes
  through resolvers; OpenVPN, IKEv2 and AmneziaWG come as profiles that name the main server.
- **Keeping it current.** A redeploy can change ports, and a move changes the address the relay forwards to. Until
  `xtune relay sync`, the configs that changed are left out of subscriptions (rather than handed out broken), and
  `xtune doctor` and the deploy output say so.
- **Device limits** count client IPs, and every user through a relay comes from the relay's IP, so devices through a
  relay are not told apart.

## Several servers

`xtune peers enable` on one server (the primary) and `xtune follow PEER_URL` on others: followers take the primary's users and report their traffic to it every minute, and the primary's subscriptions and failover config list every server's configs. When one server's IP is blocked, users fail over to another. Quotas count traffic on all servers, and so do device limits: followers report who is online, and the primary counts each user's devices on every server and has followers block the newcomers.