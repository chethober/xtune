# xtune community directory

VPNs people share with everyone: listed in the xtune app (**Configs → Community**, and **Home → the subscription
picker → Community**) and in [@chthvpnbot](https://t.me/chthvpnbot)'s 🌍 Directory. A Cloudflare Worker with a D1
database; no server to run.

## How it works

- **Sharing.** An owner shares from the app's **Manage** tab: the app makes an xtune *public pool* on the server
  (`xtune public add NAME --quota … --days … --members …`) and sends its connect link here. A subscription URL or
  share links can be shared too (`POST /v1/vpns`). The owner gets an owner key, kept in the app's secure storage, to
  edit, pause or delete it later.
- **Joining.** The app asks to join with a random key it made for itself (`POST /v1/vpns/ID/join`). For a pool, the
  directory asks the server for a member named after a hash of that key, so every device gets **its own user** with
  the pool's limits, the same one each time it asks, and the server learns nothing about who it is. For a shared
  subscription or links, everyone gets the same ones.
- **Privacy.** The secret part (the connect link, the URL, the links) is never listed. Members are hashes, and rate
  limits count a daily-salted hash of the address, never the address.
- **Health.** Every 30 minutes the directory checks the VPNs shared from the app; three failures in a row take one
  off the list until a check passes. Full pools are not listed. Five members reporting it illegal or a scam unlist it
  until an admin looks (`GET /v1/admin/reports`, `POST /v1/admin/unlist`).
- **The bot.** On its tick, @chthvpnbot sends its public VPNs (`POST /v1/bot/import`) and takes the ones shared from
  the app (`GET /v1/bot/export`), so both list the same VPNs. Each joins members its own way; the xtune server enforces
  the member cap and the limits either way. Bot admins acting on a report about an app-shared VPN unlist or delete it
  here too (`POST /v1/bot/unlist`).

The Worker reaches servers only on Cloudflare's ports (443, 2053, 2083, 2087, 2096, 8443 for HTTPS): xtune serves
connect links on 443 when it is free, and its CDN subscription ports are among these.

## API

| | |
|---|---|
| `GET /v1/vpns[?country=DE]` | the listed VPNs, most free places first |
| `POST /v1/vpns` `{source, name, description?, country?}` | share; → `{id, ownerKey, vpn}` |
| `GET /v1/vpns/ID` | one VPN |
| `GET /v1/vpns/ID/owner` | with `Authorization: Owner KEY`: status, members from the app, reports |
| `PATCH /v1/vpns/ID` `{name?, description?, country?, paused?, source?}` | owner: change it (`source`: a new connect link after `xtune public token`) |
| `DELETE /v1/vpns/ID` | owner: delete it |
| `POST /v1/vpns/ID/join` `{member}` | → `{kind: 'xtune', sub, json, status, quota, expires}` or `{kind, source}` |
| `POST /v1/vpns/ID/leave` `{member}` | a pool deletes the member's user |
| `POST /v1/vpns/ID/report` `{member, reason: illegal\|scam\|dead}` | |

`member` is 32 to 64 hex characters the app keeps; it never leaves the app other than here.

## Deploy

See the top of [wrangler.toml](wrangler.toml). Then set the directory's address in the app's release build
(`COMMUNITY_URL` in the repository's Actions variables) and the bot's `DIRECTORY_URL` and `DIRECTORY_KEY` (the
Worker's `BOT_KEY`).

## Test

```sh
node --test test/*.test.mjs   # Node 22.13 or later: the tests use node:sqlite in place of D1
```
