# Architecture

[Docs](README.md) · [Tuning](tuning.md) · [Server](server.md) · [Multi-hop](multihop.md) · [Operations](operations.md) · [Compatibility](compatibility.md) · [Architecture](architecture.md)

xtune is one static Go binary: the client on your computer and the tool on the server.

```
main (cmd, TUI, tuning, deploy, users, subscription server, enforce job)
 ├── internal/protocols   state → Xray configs, share links, subscriptions, names, VPN/tunnel profiles
 │    └── internal/state  the data model: state.json and usage.json, their schema and migrations
 │         └── internal/remote   running commands on the server: SSH or local, the lock, helper.sh
 ├── internal/netinfo     ASN and country of a network or an IP (Team Cymru, cached)
 ├── internal/cloudflare  the Cloudflare API calls xtune makes
 └── internal/dnstt       the DNS tunnel (vendored dnstt)
```

Each package only imports the ones below it. `protocols` only builds: it never talks to a server.

- **Everything that changes the server** goes through `internal/remote/helper.sh`, embedded in the binary and run as
  root, which keeps to `/opt/xray-autotune`, the `xray-autotune*` units, the `/usr/local/bin/xtune` link and the
  firewall rules it records itself ([what it touches](operations.md#what-xtune-touches-on-the-server)).
- **The same code runs over SSH and locally.** Functions that change the server take a `*remote.Server`, whose
  transport is SSH from a client or `bash` on the server itself.
- **State lives on the server.** Users are managed only there; the client reads and writes `state.json` over SSH
  when it deploys. The subscription server reads it on every request, so changes take effect at once.
- **Tests** run without a server or network: a fake transport answers the helper's commands and keeps files in
  memory, and lookups are stubbed. `XTUNE_E2E=1 go test -run EndToEnd ./...` runs a real local Xray.

The root package still holds tuning, the TUI and the subscription server; splitting them out is on the
[roadmap](../ROADMAP.md).
