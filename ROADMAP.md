# Roadmap

This is where xtune is going. It is a plan, not a promise: items move between
releases as the work and the networks change. Dates are left off on purpose.

## v1.0

The first release that promises stability, not just features.

### Features

- [ ] **MTProto proxy.** Telegram's own proxy protocol, for users who only
  need Telegram. Telegram apps connect to it natively (Settings → Data and
  Storage → Proxy, or a `t.me/proxy` link), so nothing else needs installing.
  Served by xtune itself in the tunnel server, with fake TLS ("`ee`" secrets)
  using a REALITY target as the fronted domain. Each user gets a secret of
  their own (derived from their UUID like their other credentials), so quotas,
  expiry, device limits and disabling work as for the SSH tunnels. Tuned from
  the client like the other configs, and offered as a `tg://`/`t.me` link and
  QR code on the subscription page and in @chthvpnbot.
- [x] **TUI dashboard.** A full-screen overview of a deployment — configs with
  their state (active / quiet / idle), users with quota bars and who is online,
  refreshing live — in place of the static status dump. Read-only; changes stay
  in the menus. First screen of the wider TUI redesign below.
- [ ] **TUI redesign (rest).** Grow the dashboard into the main interface:
  - a sortable, filterable users table with inline actions (enable, renew, QR);
  - a live tune view: a grid that fills in as variants finish, with a running
    leaderboard;
  - a command palette (Ctrl+K) that also shows the matching CLI command;
  - a client-side console that draws the server's state locally (fetched as
    JSON over SSH) instead of running the server's TUI over SSH;
  - Persian / RTL in the TUI, reusing the subscription pages' i18n.

### Stability

- [ ] A versioned state-file schema, with tested migrations from every 0.x
  release.
- [ ] A frozen set of CLI flags and `--json` output shapes, with a documented
  deprecation policy.
- [ ] Split the root `main` package into `internal/` packages (server
  transport, state, protocols, tuning, TUI, subscription) so the TUI rewrite
  does not fight the rest of the code.

### Docs

- [ ] A docs site. The README's feature table is doing the job of a manual;
  move the reference there and keep the README short.

## v1.1

Bigger changes, kept out of 1.0 so it can ship.

- [ ] **Two-hop through a domestic VPS**: client → in-country VPS → foreign
  server, tuning each hop separately and deploying both. Common where domestic
  routes are filtered less than international ones.
- [ ] **Rotate a config's settings, not just the server IP**: when a config is
  confirmed blocked on a network, change its SNI, port or path automatically —
  cheaper than moving the IP.
- [ ] **Time-of-day rankings**: filtering often worsens in the evening or
  around events; keep per-hour results and rank by the current hour.
- [ ] **Multi-server overview** in the client console, once it draws state
  locally.
- [ ] **Other CDN fronts** (Fastly, Gcore, …), so Cloudflare is not the only
  option.
- [ ] **Offline handout**: a printable PDF of per-user or per-pool QR codes,
  for people with no reliable way to receive a link.
- [ ] **Telegram Mini App** for management, as a richer front end than the
  bot's buttons.
