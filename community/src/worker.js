// The xtune community directory: VPNs people share with everyone, listed in the xtune app (the
// Configs tab and Home's subscriptions) and in @chthvpnbot's 🌍 Directory.
//
// Owners share from the app's Manage tab (an xtune public pool: every member gets their own
// user on the server) or by sending a subscription URL or share links. The directory keeps the
// secret part (the pool's connect link, the URL, the links) and hands members only what they
// need. It never learns who anyone is: members are a hash of a random key their app made, and
// rate limits count a daily-salted hash of the address, never the address.
//
// The bot and the directory sync every 30 minutes (the bot's tick): the bot sends its public
// VPNs, and takes the ones shared from the app. Each side joins members its own way, and the
// xtune server enforces the member cap and the limits either way.
//
// Bindings (wrangler.toml): DB (D1). Secrets: BOT_KEY (the bot's sync key), ADMIN_KEY.

import { clean, COUNTRY_RE, DESC_MAX, detectSource, labelFrom, NAME_MAX, parseSubscription, protocolsOf } from './sources.js';

const DAY = 86400;
const FAILS_DOWN = 3; // health checks failed in a row before a VPN leaves the list
const CHECK_EVERY = 25 * 60;
const CHECKS_PER_RUN = 40;
const REPORTS_UNLIST = 5; // illegal/scam reports from different members: unlisted until an admin looks
const LIMITS = { share: [5, DAY], join: [30, 3600], report: [20, DAY], edit: [60, 3600] };
const KINDS = ['xtune', 'sub', 'links'];

const now = () => Math.floor(Date.now() / 1000);

async function sha256(s) {
  const b = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(s));
  return [...new Uint8Array(b)].map((x) => x.toString(16).padStart(2, '0')).join('');
}

function randomId(n, alphabet = 'abcdefghijkmnpqrstuvwxyz23456789') {
  const b = crypto.getRandomValues(new Uint8Array(n));
  return [...b].map((x) => alphabet[x % alphabet.length]).join('');
}

const randomHex = (n) => [...crypto.getRandomValues(new Uint8Array(n))].map((x) => x.toString(16).padStart(2, '0')).join('');

class Refusal extends Error {
  constructor(status, code, message) {
    super(message ?? code);
    this.status = status;
    this.code = code;
  }
}

function json(v, status = 200) {
  return new Response(JSON.stringify(v), {
    status,
    headers: { 'content-type': 'application/json; charset=utf-8', 'access-control-allow-origin': '*', 'cache-control': 'no-store' },
  });
}

async function body(req) {
  try {
    const t = await req.text();
    if (t.length > 16384) throw new Refusal(413, 'tooLarge');
    return t ? JSON.parse(t) : {};
  } catch (e) {
    if (e instanceof Refusal) throw e;
    throw new Refusal(400, 'badJson');
  }
}

// ---------------------------------------------------------------- the xtune pool API

// GET <link> for the pool, POST <link>/join and <link>/leave with { label } (xtune's public.go).
async function pool(link, path = '', payload) {
  const init = payload ? { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(payload) } : {};
  let res;
  try {
    res = await fetch(link + path, init).catch(() => fetch(link + path, init));
  } catch {
    return { ok: false, error: 'unreachable' };
  }
  let data = null;
  try {
    data = await res.json();
  } catch { /* not an xtune server */ }
  if (res.ok && data) return { ok: true, data };
  const error = res.status === 404 ? 'gone' : res.status === 409 ? (data?.error ?? 'full') : res.status === 429 ? 'busy' : 'unreachable';
  return { ok: false, error };
}

// What the source says about itself: protocols and, for a pool, its limits and members.
async function inspect(kind, source) {
  if (kind === 'xtune') {
    const r = await pool(source);
    if (!r.ok) return r;
    const d = r.data;
    return {
      ok: true,
      fields: {
        protocols: Array.isArray(d.protocols) ? d.protocols.slice(0, 12).map(String) : [],
        active: Number(d.active) || 0,
        max: Number(d.max) || 0,
        quota_gb: Number(d.quotaGB) || 0,
        days: Number(d.days) || 0,
        devices: Number(d.devices) || 0,
        paused: d.paused ? 1 : 0,
      },
    };
  }
  if (kind === 'sub') {
    let res;
    try {
      res = await fetch(source, { headers: { 'user-agent': 'xtune-community' } });
    } catch {
      return { ok: false, error: 'unreachable' };
    }
    if (!res.ok) return { ok: false, error: res.status === 404 ? 'gone' : 'unreachable' };
    const links = parseSubscription(await res.text());
    if (!links.length) return { ok: false, error: 'empty' };
    return { ok: true, fields: { protocols: protocolsOf(links.join('\n')) } };
  }
  return { ok: true, fields: { protocols: protocolsOf(source) } };
}

// ---------------------------------------------------------------- rows

const LISTED = "status = 'ok' AND paused = 0 AND unlisted = 0";

function publicView(v) {
  return {
    id: v.id,
    origin: v.origin,
    kind: v.kind,
    name: v.name,
    description: v.description,
    country: v.country,
    protocols: JSON.parse(v.protocols || '[]'),
    active: v.active,
    max: v.max,
    quotaGB: v.quota_gb,
    days: v.days,
    devices: v.devices,
    checkedAt: v.checked_at,
    createdAt: v.created_at,
  };
}

async function getVpn(env, id) {
  if (!/^[a-z0-9]{6,16}$/.test(String(id))) return null;
  return env.DB.prepare('SELECT * FROM vpns WHERE id = ?').bind(id).first();
}

async function limited(env, req, action) {
  const [n, per] = LIMITS[action];
  const day = Math.floor(now() / DAY);
  const ip = req.headers.get('cf-connecting-ip') ?? 'unknown';
  const who = (await sha256(`${day}:${ip}:${env.SALT ?? ''}`)).slice(0, 24);
  const since = now() - per;
  const row = await env.DB.prepare('SELECT COUNT(*) AS n FROM hits WHERE who = ? AND action = ? AND at > ?').bind(who, action, since).first();
  if ((row?.n ?? 0) >= n) throw new Refusal(429, 'busy', 'too many requests, try again later');
  await env.DB.prepare('INSERT INTO hits (who, action, at) VALUES (?, ?, ?)').bind(who, action, now()).run();
}

async function ownerOf(env, req, id) {
  const v = await getVpn(env, id);
  if (!v) throw new Refusal(404, 'gone');
  const key = /^Owner ([0-9a-f]{32,64})$/.exec(req.headers.get('authorization') ?? '')?.[1];
  const admin = isAdmin(env, req);
  if (!admin && (!key || !v.owner_hash || (await sha256(key)) !== v.owner_hash)) throw new Refusal(403, 'notOwner');
  return v;
}

function bearer(req, scheme) {
  return new RegExp(`^${scheme} (\\S{16,})$`).exec(req.headers.get('authorization') ?? '')?.[1];
}

function isAdmin(env, req) {
  const k = bearer(req, 'Admin');
  return !!(env.ADMIN_KEY && k && k === env.ADMIN_KEY);
}

function isBot(env, req) {
  const k = bearer(req, 'Bot');
  return !!(env.BOT_KEY && k && k === env.BOT_KEY);
}

// Deletes a VPN. One shared from the app is remembered as gone, so the bot drops its copy.
async function deleteVpn(env, id) {
  await env.DB.batch([
    env.DB.prepare("INSERT OR REPLACE INTO gone (id, at) SELECT id, ? FROM vpns WHERE id = ? AND origin = 'app'").bind(now(), id),
    env.DB.prepare('DELETE FROM members WHERE vpn_id = ?').bind(id),
    env.DB.prepare('DELETE FROM reports WHERE vpn_id = ?').bind(id),
    env.DB.prepare('DELETE FROM vpns WHERE id = ?').bind(id),
  ]);
}

// ---------------------------------------------------------------- handlers

async function list(env, url) {
  const country = url.searchParams.get('country');
  const q = COUNTRY_RE.test(country ?? '')
    ? env.DB.prepare(`SELECT * FROM vpns WHERE ${LISTED} AND country = ? ORDER BY created_at DESC LIMIT 500`).bind(country)
    : env.DB.prepare(`SELECT * FROM vpns WHERE ${LISTED} ORDER BY created_at DESC LIMIT 500`);
  const rows = (await q.all()).results ?? [];
  // most free places first (no cap counts as most), then newest
  const free = (v) => (v.max > 0 ? v.max - v.active : Infinity);
  rows.sort((a, b) => free(b) - free(a) || b.created_at - a.created_at);
  return json({ vpns: rows.filter((v) => free(v) > 0).map(publicView) });
}

async function share(env, req) {
  const b = await body(req);
  const d = detectSource(b.source);
  if (d.error) throw new Refusal(400, d.error, d.error === 'port' ? `the directory reaches only Cloudflare's ports, not ${d.port}` : undefined);
  if (b.kind && b.kind !== d.kind) throw new Refusal(400, 'kind');
  const name = clean(b.name, NAME_MAX);
  if (name.length < 2) throw new Refusal(400, 'name');
  const country = COUNTRY_RE.test(b.country ?? '') ? b.country : '';
  await limited(env, req, 'share');
  if (await env.DB.prepare('SELECT id FROM vpns WHERE source = ?').bind(d.source).first()) throw new Refusal(409, 'already', 'this VPN is already listed');
  const r = await inspect(d.kind, d.source);
  if (!r.ok) throw new Refusal(422, r.error, `the directory could not reach it (${r.error})`);
  const id = randomId(10);
  const ownerKey = randomHex(16);
  const t = now();
  const f = r.fields;
  await env.DB.prepare(
    `INSERT INTO vpns (id, origin, owner_hash, kind, source, name, description, country, protocols, active, max, quota_gb, days, devices,
       paused, checked_at, created_at, updated_at) VALUES (?, 'app', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
  ).bind(id, await sha256(ownerKey), d.kind, d.source, name, clean(b.description, DESC_MAX), country, JSON.stringify(f.protocols ?? []),
    f.active ?? 0, f.max ?? 0, f.quota_gb ?? 0, f.days ?? 0, f.devices ?? 0, f.paused ?? 0, t, t, t).run();
  return json({ id, ownerKey, vpn: publicView(await getVpn(env, id)) }, 201);
}

async function ownerView(env, req, id) {
  const v = await ownerOf(env, req, id);
  const members = await env.DB.prepare('SELECT COUNT(*) AS n FROM members WHERE vpn_id = ?').bind(id).first();
  const reports = await env.DB.prepare('SELECT reason, COUNT(*) AS n FROM reports WHERE vpn_id = ? GROUP BY reason').bind(id).all();
  return json({
    vpn: { ...publicView(v), status: v.status, paused: !!v.paused, unlisted: !!v.unlisted, fails: v.fails },
    appMembers: members?.n ?? 0,
    reports: Object.fromEntries((reports.results ?? []).map((r) => [r.reason, r.n])),
  });
}

async function edit(env, req, id) {
  const v = await ownerOf(env, req, id);
  await limited(env, req, 'edit');
  const b = await body(req);
  const set = {};
  if (b.name !== undefined) {
    set.name = clean(b.name, NAME_MAX);
    if (set.name.length < 2) throw new Refusal(400, 'name');
  }
  if (b.description !== undefined) set.description = clean(b.description, DESC_MAX);
  if (b.country !== undefined) set.country = COUNTRY_RE.test(b.country ?? '') ? b.country : '';
  if (b.paused !== undefined) set.paused = b.paused ? 1 : 0;
  if (b.source !== undefined) {
    // a new connect link (xtune public token) or new links: members of shared links keep the old ones
    const d = detectSource(b.source);
    if (d.error || d.kind !== v.kind) throw new Refusal(400, d.error ?? 'kind');
    const r = await inspect(d.kind, d.source);
    if (!r.ok) throw new Refusal(422, r.error);
    set.source = d.source;
    set.protocols = JSON.stringify(r.fields.protocols ?? []);
    set.status = 'ok';
    set.fails = 0;
  }
  const keys = Object.keys(set);
  if (keys.length) {
    await env.DB.prepare(`UPDATE vpns SET ${keys.map((k) => `${k} = ?`).join(', ')}, updated_at = ? WHERE id = ?`)
      .bind(...keys.map((k) => set[k]), now(), id).run();
  }
  return json({ vpn: publicView(await getVpn(env, id)) });
}

async function remove(env, req, id) {
  const v = await ownerOf(env, req, id);
  await deleteVpn(env, v.id);
  return json({ ok: true });
}

async function memberOf(b, id) {
  if (!/^[0-9a-f]{32,64}$/.test(String(b.member ?? ''))) throw new Refusal(400, 'member');
  const hash = await sha256(`${id}:${b.member}`);
  return { hash, label: labelFrom(hash) };
}

async function join(env, req, id) {
  const v = await getVpn(env, id);
  if (!v || v.unlisted || v.status !== 'ok') throw new Refusal(404, 'gone');
  const b = await body(req);
  const m = await memberOf(b, id);
  await limited(env, req, 'join');
  const known = await env.DB.prepare('SELECT label FROM members WHERE vpn_id = ? AND member_hash = ?').bind(id, m.hash).first();
  if (v.paused && !known) throw new Refusal(409, 'paused');
  let out;
  if (v.kind === 'xtune') {
    // joining again returns the same user (and renews one whose time ran out)
    const r = await pool(v.source, '/join', { label: m.label });
    if (!r.ok) throw new Refusal(r.error === 'gone' ? 404 : r.error === 'unreachable' ? 502 : 409, r.error);
    out = { kind: 'xtune', sub: r.data.sub, json: r.data.json, status: r.data.status, quota: r.data.quota, expires: r.data.expires };
  } else {
    out = { kind: v.kind, source: v.source };
  }
  if (!known) {
    await env.DB.prepare('INSERT OR IGNORE INTO members (vpn_id, member_hash, label, joined_at) VALUES (?, ?, ?, ?)')
      .bind(id, m.hash, m.label, now()).run();
  }
  return json({ ...out, name: v.name, id: v.id });
}

async function leave(env, req, id) {
  const v = await getVpn(env, id);
  const b = await body(req);
  const m = await memberOf(b, id);
  if (v?.kind === 'xtune') await pool(v.source, '/leave', { label: m.label }); // gone already is fine
  await env.DB.prepare('DELETE FROM members WHERE vpn_id = ? AND member_hash = ?').bind(id, m.hash).run();
  return json({ ok: true });
}

async function report(env, req, id) {
  const v = await getVpn(env, id);
  if (!v) throw new Refusal(404, 'gone');
  const b = await body(req);
  if (!['illegal', 'scam', 'dead'].includes(b.reason)) throw new Refusal(400, 'reason');
  const m = await memberOf(b, id);
  await limited(env, req, 'report');
  await env.DB.prepare('INSERT OR REPLACE INTO reports (vpn_id, member_hash, reason, at) VALUES (?, ?, ?, ?)').bind(id, m.hash, b.reason, now()).run();
  const bad = await env.DB.prepare("SELECT COUNT(*) AS n FROM reports WHERE vpn_id = ? AND reason IN ('illegal', 'scam')").bind(id).first();
  if ((bad?.n ?? 0) >= REPORTS_UNLIST) {
    await env.DB.prepare('UPDATE vpns SET unlisted = 1, updated_at = ? WHERE id = ?').bind(now(), id).run();
  }
  return json({ ok: true });
}

// ---------------------------------------------------------------- the bot

// GET /v1/bot/export?since=T: the VPNs shared from the app (with their sources: the bot joins
// members itself), changed since T, and the ones deleted since.
async function botExport(env, url) {
  const since = Number(url.searchParams.get('since')) || 0;
  const rows = (await env.DB.prepare("SELECT * FROM vpns WHERE origin = 'app' AND updated_at >= ? ORDER BY updated_at LIMIT 1000").bind(since).all()).results ?? [];
  const gone = (await env.DB.prepare('SELECT id FROM gone WHERE at >= ?').bind(since).all()).results ?? [];
  return json({
    now: now(),
    vpns: rows.map((v) => ({ ...publicView(v), source: v.source, status: v.status, paused: !!v.paused, unlisted: !!v.unlisted })),
    gone: gone.map((g) => g.id),
  });
}

// POST /v1/bot/import { vpns: [{ botId, kind, source, name, description, country, protocols, active, max,
// quotaGb, days, devices, status, listed }], all: true }: the bot's public VPNs. With all, the bot's
// VPNs not in the list are removed.
async function botImport(env, req) {
  const b = await body(req);
  const list = Array.isArray(b.vpns) ? b.vpns.slice(0, 1000) : [];
  const t = now();
  const seen = [];
  let added = 0, updated = 0;
  for (const x of list) {
    const botId = Number(x.botId);
    if (!Number.isInteger(botId) || botId <= 0 || !KINDS.includes(x.kind)) continue;
    const d = detectSource(x.source);
    if (d.error || d.kind !== x.kind) continue; // the directory cannot serve it (a port Workers do not reach)
    seen.push(botId);
    const fields = [x.kind, d.source, clean(x.name, NAME_MAX) || 'VPN', clean(x.description, DESC_MAX), COUNTRY_RE.test(x.country ?? '') ? x.country : '',
      JSON.stringify(Array.isArray(x.protocols) ? x.protocols.slice(0, 12).map(String) : []), Number(x.active) || 0, Number(x.max) || 0,
      Number(x.quotaGb) || 0, Number(x.days) || 0, Number(x.devices) || 0, x.status === 'ok' ? 'ok' : 'down', x.listed === false ? 1 : 0];
    const old = await env.DB.prepare('SELECT id, source FROM vpns WHERE bot_id = ?').bind(botId).first();
    if (old) {
      await env.DB.prepare(`UPDATE vpns SET kind = ?, source = ?, name = ?, description = ?, country = ?, protocols = ?, active = ?, max = ?,
          quota_gb = ?, days = ?, devices = ?, status = ?, paused = ?, checked_at = ?, updated_at = ? WHERE id = ?`)
        .bind(...fields, t, t, old.id).run();
      updated++;
    } else {
      // one shared from the app and also added to the bot by hand: the app's row stays, the bot's copy is skipped
      if (await env.DB.prepare('SELECT id FROM vpns WHERE source = ?').bind(d.source).first()) continue;
      await env.DB.prepare(`INSERT INTO vpns (id, origin, bot_id, kind, source, name, description, country, protocols, active, max, quota_gb,
          days, devices, status, paused, checked_at, created_at, updated_at) VALUES (?, 'bot', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
        .bind(randomId(10), botId, ...fields, t, t, t).run();
      added++;
    }
  }
  let removed = 0;
  if (b.all) {
    const rows = (await env.DB.prepare("SELECT id, bot_id FROM vpns WHERE origin = 'bot'").all()).results ?? [];
    for (const r of rows) {
      if (!seen.includes(r.bot_id)) {
        await deleteVpn(env, r.id);
        removed++;
      }
    }
  }
  return json({ added, updated, removed });
}

// POST /v1/bot/unlist { id, unlisted }: a bot admin acted on a report about a VPN shared from the app.
async function botUnlist(env, req) {
  const b = await body(req);
  const v = await getVpn(env, b.id);
  if (!v) throw new Refusal(404, 'gone');
  if (b.delete) await deleteVpn(env, v.id);
  else await env.DB.prepare('UPDATE vpns SET unlisted = ?, updated_at = ? WHERE id = ?').bind(b.unlisted === false ? 0 : 1, now(), v.id).run();
  return json({ ok: true });
}

// ---------------------------------------------------------------- health checks

// Every run checks the app's VPNs checked longest ago (the bot checks its own). A VPN failing
// FAILS_DOWN times in a row leaves the list, and comes back by itself when a check passes.
export async function runChecks(env) {
  const rows = (await env.DB.prepare("SELECT * FROM vpns WHERE origin = 'app' AND kind != 'links' AND checked_at < ? ORDER BY checked_at LIMIT ?")
    .bind(now() - CHECK_EVERY, CHECKS_PER_RUN).all()).results ?? [];
  let ok = 0, failed = 0;
  for (const v of rows) {
    const r = await inspect(v.kind, v.source);
    const t = now();
    if (r.ok) {
      const f = r.fields;
      await env.DB.prepare(`UPDATE vpns SET status = 'ok', fails = 0, checked_at = ?, protocols = ?, active = ?, max = ?, quota_gb = ?, days = ?,
          devices = ?, updated_at = CASE WHEN status != 'ok' OR active != ? OR max != ? THEN ? ELSE updated_at END WHERE id = ?`)
        .bind(t, JSON.stringify(f.protocols ?? []), f.active ?? v.active, f.max ?? v.max, f.quota_gb ?? v.quota_gb, f.days ?? v.days,
          f.devices ?? v.devices, f.active ?? v.active, f.max ?? v.max, t, v.id).run();
      ok++;
    } else if (r.error === 'gone') {
      // the pool was deleted or its link rotated: the owner can send the new one
      await env.DB.prepare("UPDATE vpns SET status = 'down', fails = ?, checked_at = ?, updated_at = ? WHERE id = ?").bind(FAILS_DOWN, t, t, v.id).run();
      failed++;
    } else {
      const fails = v.fails + 1;
      const status = fails >= FAILS_DOWN ? 'down' : v.status;
      // a change of status is a change the bot picks up (updated_at)
      await env.DB.prepare('UPDATE vpns SET fails = ?, status = ?, checked_at = ?, updated_at = ? WHERE id = ?')
        .bind(fails, status, t, status !== v.status ? t : v.updated_at, v.id).run();
      failed++;
    }
  }
  await env.DB.batch([
    env.DB.prepare('DELETE FROM hits WHERE at < ?').bind(now() - 2 * DAY),
    env.DB.prepare('DELETE FROM gone WHERE at < ?').bind(now() - 30 * DAY),
  ]);
  return { checked: rows.length, ok, failed };
}

// ---------------------------------------------------------------- routing

export async function handle(req, env) {
  const url = new URL(req.url);
  const parts = url.pathname.replace(/\/+$/, '').split('/').filter(Boolean);
  const m = req.method;
  if (m === 'OPTIONS') {
    return new Response(null, { headers: { 'access-control-allow-origin': '*', 'access-control-allow-methods': 'GET, POST, PATCH, DELETE',
      'access-control-allow-headers': 'content-type, authorization' } });
  }
  try {
    if (parts[0] !== 'v1') {
      if (parts.length === 0) return json({ name: 'xtune community directory', api: '/v1/vpns' });
      throw new Refusal(404, 'notFound');
    }
    const [, a, id, action] = parts;
    if (a === 'vpns' && !id && m === 'GET') return await list(env, url);
    if (a === 'vpns' && !id && m === 'POST') return await share(env, req);
    if (a === 'vpns' && id && !action && m === 'GET') {
      const v = await getVpn(env, id);
      if (!v || v.unlisted) throw new Refusal(404, 'gone');
      return json({ vpn: { ...publicView(v), status: v.status, paused: !!v.paused } });
    }
    if (a === 'vpns' && id && !action && m === 'PATCH') return await edit(env, req, id);
    if (a === 'vpns' && id && !action && m === 'DELETE') return await remove(env, req, id);
    if (a === 'vpns' && id && action === 'owner' && m === 'GET') return await ownerView(env, req, id);
    if (a === 'vpns' && id && action === 'join' && m === 'POST') return await join(env, req, id);
    if (a === 'vpns' && id && action === 'leave' && m === 'POST') return await leave(env, req, id);
    if (a === 'vpns' && id && action === 'report' && m === 'POST') return await report(env, req, id);
    if (a === 'bot') {
      if (!isBot(env, req)) throw new Refusal(403, 'notBot');
      if (id === 'export' && m === 'GET') return await botExport(env, url);
      if (id === 'import' && m === 'POST') return await botImport(env, req);
      if (id === 'unlist' && m === 'POST') return await botUnlist(env, req);
    }
    if (a === 'admin') {
      if (!isAdmin(env, req)) throw new Refusal(403, 'notAdmin');
      if (id === 'reports' && m === 'GET') {
        const r = await env.DB.prepare(`SELECT v.id, v.name, v.origin, v.unlisted, r.reason, COUNT(*) AS n FROM reports r JOIN vpns v ON v.id = r.vpn_id
            GROUP BY v.id, r.reason ORDER BY n DESC LIMIT 200`).all();
        return json({ reports: r.results ?? [] });
      }
      if (id === 'unlist' && m === 'POST') return await botUnlist(env, req);
    }
    throw new Refusal(404, 'notFound');
  } catch (e) {
    if (e instanceof Refusal) return json({ error: e.code, message: e.message }, e.status);
    console.error(e);
    return json({ error: 'internal' }, 500);
  }
}

export default {
  fetch: (req, env) => handle(req, env),
  scheduled: (_event, env, ctx) => ctx.waitUntil(runChecks(env)),
};
