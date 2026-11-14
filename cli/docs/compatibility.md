# Compatibility: what 1.x promises

[Docs](README.md) · [Tuning](tuning.md) · [Server](server.md) · [Multi-hop](multihop.md) · [Operations](operations.md) · [Compatibility](compatibility.md) · [Architecture](architecture.md)

From 1.0, xtune follows [Semantic Versioning](https://semver.org/). Within 1.x, these stay compatible:

## Files on the server

`state.json` and `usage.json` (in `/opt/xray-autotune`) carry a schema number (`"schema": 1`).

- Every 1.x release reads the files of every earlier release, including all of 0.x, and migrates them in memory
  when it reads them. Tests read what each release from 0.1.0 on wrote, with every field set, and fail if a field is
  lost on the way (`pkg/state/testdata`).
- A release that changes what a field means raises the schema and ships a migration. New fields need neither:
  older files lack them, which reads as their default.
- A file with a **newer** schema is refused ("update xtune"), not rewritten: an older xtune would drop what it does
  not know. Update xtune on your computer and on the server (`xtune self-update`, or deploy/console from an updated
  client).
- Backups (`xtune backup`) go through the same migrations, so a backup from any release restores.

## Go packages

The packages under `pkg/` are importable, for the xtune client app and other tools, but their Go API is not frozen
in 1.x: it can change in a minor release. What they read and write (the files above, links, subscriptions) is
covered.

## Commands and flags

- The commands, subcommands and flags in `xtune help` stay, with the same meaning. New ones may be added. The
  help text is checked against `testdata/usage.txt`, so a change to it is always deliberate.
- Human-readable output (tables, messages, the order of lines) may change in any release. Scripts should use
  `--json`.

## `--json` output

`xtune users list --json` is a list of:

```json
{"name": "carol", "status": "active", "enabled": true, "up": 0, "down": 0, "quota": 0, "expires": 0,
 "devices": 0, "online": [], "lastSeen": 0, "subscription": "https://…", "reset": "monthly", "resetAt": 0}
```

(`reset`, `resetAt` and `subscription` only when set.) `xtune users links NAME --json` is:

```json
{"name": "carol", "enabled": true, "links": ["vless://…"], "subscription": "https://…", "failover": "https://…/json"}
```

Keys are not removed or renamed within 1.x; new keys may be added, so ignore keys you do not know. Byte counts are
bytes, times are Unix seconds, 0 means unlimited or never.

## Links, subscriptions and management links

- Users' subscription links keep working across 1.x updates, moves (`xtune migrate`) and redeploys.
- The subscription body (base64 share links), the `Subscription-Userinfo` and `Profile-Title` headers and the
  failover config at `/json` keep their formats.
- Connect and management links for [@chthvpnbot](https://t.me/chthvpnbot) keep their API.

## Deprecation

When something above has to change, a 1.x release first deprecates it: the old form keeps working, xtune says
in its output what replaces it, and `CHANGELOG.md` lists it under *Deprecated*. It is removed only in the next
major release (2.0), and never sooner than one minor release after the deprecation. Security fixes are the
exception: they ship when they are ready, and the changelog says what they changed.
