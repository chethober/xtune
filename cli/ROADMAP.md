# Roadmap

This is where xtune is going. It is a plan, not a promise: items move between
releases as the work and the networks change. Dates are left off on purpose.

## 1.0

The first release that promises stability, not just features.

- [x] **MTProto proxy**, with per-user secrets and `t.me/proxy` links.
- [x] **TUI dashboard**: configs with their state, users with quota bars and who is online.
- [x] **Multi-hop through a relay** in the users' own country (`xtune relay`), measured from the relay. (Planned
  for 1.1; it fitted in 1.0 as a port forwarder.)
- [x] **Country flags and named configs and subscriptions** (`xtune names`), with named one-tap import links.
- [x] A versioned state-file schema, with tested migrations from every 0.x release.
- [x] A frozen set of CLI flags and `--json` output shapes, with a documented deprecation policy
  ([docs/compatibility.md](docs/compatibility.md)), checked by tests.
- [x] The reference moved out of the README into [docs/](docs/README.md).
- [x] Split the root package, first part: `internal/remote` (server transport, now `pkg/remote`), `internal/state` (data model and
  storage), `internal/protocols` (configs, links, subscriptions, profiles), `internal/netinfo`,
  `internal/cloudflare`.

## 1.x

- [ ] **Split the root package, rest**: tuning, the subscription server and the TUI. Tuning first: it still
  reaches into the doctor checks, the Worker deployment and Xray installation, which need their own packages.
- [ ] **TUI redesign**, growing the dashboard into the main interface:
  - a sortable, filterable users table with inline actions (enable, renew, QR);
  - a live tune view: a grid that fills in as variants finish, with a running leaderboard;
  - a command palette (Ctrl+K) that also shows the matching CLI command;
  - a client-side console that draws the server's state locally (fetched as JSON over SSH) instead of running the
    server's TUI over SSH;
  - Persian / RTL in the TUI, reusing the subscription pages' i18n.
- [ ] **Relays, second hop tuned**: let the relay → server hop use a different config than the one clients use
  (tuned from the relay), for routes where the international leg is filtered differently.
- [ ] **Rotate a config's settings, not just the server IP**: when a config is confirmed blocked on a network,
  change its SNI, port or path automatically, which is cheaper than moving the IP.
- [ ] **Time-of-day rankings**: filtering often worsens in the evening or around events; keep per-hour results and
  rank by the current hour.
- [ ] **Multi-server overview** in the client console, once it draws state locally.
- [ ] **Other CDN fronts** (Fastly, Gcore, …), so Cloudflare is not the only option.
- [ ] **Offline handout**: a printable PDF of per-user or per-pool QR codes, for people with no reliable way to
  receive a link.
- [ ] **Telegram Mini App** for management, as a richer front end than the bot's buttons.
- [ ] A docs site built from `docs/`.
