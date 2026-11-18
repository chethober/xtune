-- The xtune community directory (Cloudflare D1). Apply with:
--   npx wrangler d1 execute xtune-community --remote --file schema.sql
-- Safe to run again: everything is IF NOT EXISTS.

-- One row per shared VPN. origin: 'app' (shared from the xtune app) | 'bot' (from @chthvpnbot).
-- kind: 'xtune' (a public pool: every member gets their own user) | 'sub' (a subscription URL)
--     | 'links' (share links). source is the secret: the pool's connect link, the subscription URL
-- or the links. It is never listed; members get their own subscription (xtune) or the source.
CREATE TABLE IF NOT EXISTS vpns (
  id          TEXT PRIMARY KEY,
  origin      TEXT NOT NULL,
  bot_id      INTEGER,
  owner_hash  TEXT,
  kind        TEXT NOT NULL,
  source      TEXT NOT NULL,
  name        TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  country     TEXT NOT NULL DEFAULT '',
  protocols   TEXT NOT NULL DEFAULT '[]',
  active      INTEGER NOT NULL DEFAULT 0,
  max         INTEGER NOT NULL DEFAULT 0,
  quota_gb    REAL NOT NULL DEFAULT 0,
  days        INTEGER NOT NULL DEFAULT 0,
  devices     INTEGER NOT NULL DEFAULT 0,
  status      TEXT NOT NULL DEFAULT 'ok',
  fails       INTEGER NOT NULL DEFAULT 0,
  checked_at  INTEGER NOT NULL DEFAULT 0,
  paused      INTEGER NOT NULL DEFAULT 0,
  unlisted    INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS uidx_vpns_source ON vpns(source);
CREATE UNIQUE INDEX IF NOT EXISTS uidx_vpns_bot ON vpns(bot_id) WHERE bot_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_vpns_updated ON vpns(updated_at);

-- App members: a hash of the device's member key per VPN, and the label the pool knows them by.
CREATE TABLE IF NOT EXISTS members (
  vpn_id      TEXT NOT NULL,
  member_hash TEXT NOT NULL,
  label       TEXT NOT NULL,
  joined_at   INTEGER NOT NULL,
  PRIMARY KEY (vpn_id, member_hash)
);

-- reason: illegal | scam | dead. One per member and VPN.
CREATE TABLE IF NOT EXISTS reports (
  vpn_id      TEXT NOT NULL,
  member_hash TEXT NOT NULL,
  reason      TEXT NOT NULL,
  at          INTEGER NOT NULL,
  PRIMARY KEY (vpn_id, member_hash)
);

-- Rate limits: a hash of the caller's address (salted per day, never the address) per action.
CREATE TABLE IF NOT EXISTS hits (
  who    TEXT NOT NULL,
  action TEXT NOT NULL,
  at     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_hits ON hits(who, action, at);

-- VPNs deleted, so the bot drops its copies (kept 30 days).
CREATE TABLE IF NOT EXISTS gone (
  id TEXT PRIMARY KEY,
  at INTEGER NOT NULL
);
