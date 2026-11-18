// node --test community/test — the Worker against node:sqlite (in place of D1) and a fake
// xtune pool and subscription server (in place of fetch).
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { DatabaseSync } from 'node:sqlite';
import { beforeEach, test } from 'node:test';

import { handle, runChecks } from '../src/worker.js';
import { detectSource, labelFrom, parseSubscription, protocolsOf } from '../src/sources.js';

// --- D1 on node:sqlite ---------------------------------------------------------------------------

function d1() {
  const db = new DatabaseSync(':memory:');
  db.exec(readFileSync(new URL('../schema.sql', import.meta.url), 'utf8'));
  const stmt = (sql, args = []) => ({
    bind: (...a) => stmt(sql, a),
    first: async () => db.prepare(sql).get(...args) ?? null,
    all: async () => ({ results: db.prepare(sql).all(...args) }),
    run: async () => db.prepare(sql).run(...args),
  });
  return { prepare: (sql) => stmt(sql), batch: async (list) => Promise.all(list.map((s) => s.run())), raw: db };
}

// --- the network ---------------------------------------------------------------------------------

const LINK = 'https://sub.example.com:2053/p/pub/' + 'a'.repeat(32);
let poolState;
const calls = [];

globalThis.fetch = async (url, init = {}) => {
  calls.push({ url, method: init.method ?? 'GET', body: init.body ? JSON.parse(init.body) : null });
  const reply = (v, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'content-type': 'application/json' } });
  if (url === LINK) {
    if (poolState.down) throw new Error('connect ECONNREFUSED');
    if (poolState.gone) return new Response('404', { status: 404 });
    return reply({ name: 'community', paused: false, active: poolState.active, max: 100, protocols: ['VLESS REALITY', 'Hysteria2'],
      quotaGB: 20, days: 30, devices: 2, members: [] });
  }
  if (url === LINK + '/join') {
    const { label } = JSON.parse(init.body);
    if (poolState.active >= 100) return reply({ error: 'full' }, 409);
    poolState.active++;
    return reply({ label, sub: `https://sub.example.com:2053/p/${label}`, json: `https://sub.example.com:2053/p/${label}/json`, status: 'active', quota: 20 << 30, expires: 1 });
  }
  if (url === LINK + '/leave') {
    poolState.active--;
    return reply({ ok: true });
  }
  if (url === 'https://panel.example.net/sub/xyz') {
    return new Response(btoa('vless://u@h:443?security=reality#a\nss://x@h:8388#b\n'));
  }
  return new Response('nope', { status: 404 });
};

let env;
beforeEach(() => {
  env = { DB: d1(), BOT_KEY: 'bot-key-0123456789abcdef', ADMIN_KEY: 'admin-key-0123456789abcd' };
  poolState = { active: 3 };
  calls.length = 0;
});

async function api(method, path, body, headers = {}) {
  const res = await handle(new Request('https://dir.example' + path, {
    method,
    headers: { 'content-type': 'application/json', 'cf-connecting-ip': headers.ip ?? '198.51.100.1', ...headers },
    body: body === undefined ? undefined : JSON.stringify(body),
  }), env);
  return { status: res.status, data: await res.json() };
}

const member = (n) => n.toString(16).padStart(32, '0');

// --- tests ---------------------------------------------------------------------------------------

test('sources: the same rules as the bot', () => {
  assert.equal(detectSource(LINK).kind, 'xtune');
  assert.equal(detectSource('https://panel.example.net/sub/xyz').kind, 'sub');
  assert.equal(detectSource('vless://a@b:443\nss://c@d:1').kind, 'links');
  assert.equal(detectSource('https://x.example:12345/p/pub/' + 'a'.repeat(32)).error, 'port');
  assert.equal(detectSource('hello').error, 'unknown');
  assert.deepEqual(protocolsOf('vless://u@h:443?security=reality#a\nvless://u@h:80#b\nhy2://x@y:1'), ['VLESS REALITY', 'VLESS', 'Hysteria2']);
  assert.equal(parseSubscription(btoa('vless://a@b:1\njunk\nss://c@d:2')).length, 2);
  assert.match(labelFrom('00ff'.repeat(16)), /^[a-z0-9]{10}$/, 'xtune takes labels of 4-16 letters and digits');
});

test('share a pool, list it, join it, leave it', async () => {
  let r = await api('POST', '/v1/vpns', { source: LINK, name: '  Frankfurt  community ', country: 'DE', description: 'fast\nand free' });
  assert.equal(r.status, 201, JSON.stringify(r.data));
  const { id, ownerKey } = r.data;
  assert.equal(r.data.vpn.name, 'Frankfurt community');
  assert.equal(r.data.vpn.description, 'fast and free');
  assert.deepEqual(r.data.vpn.protocols, ['VLESS REALITY', 'Hysteria2']);
  assert.equal(r.data.vpn.max, 100);

  r = await api('GET', '/v1/vpns');
  assert.equal(r.data.vpns.length, 1);
  assert.equal(r.data.vpns[0].id, id);
  assert.equal(JSON.stringify(r.data).includes('/pub/'), false, 'the connect link is never listed');

  r = await api('POST', `/v1/vpns/${id}/join`, { member: member(1) });
  assert.equal(r.status, 200, JSON.stringify(r.data));
  assert.equal(r.data.kind, 'xtune');
  assert.match(r.data.sub, /^https:\/\/sub\.example\.com:2053\/p\/[a-z0-9]{10}$/);
  const label = calls.find((c) => c.url.endsWith('/join')).body.label;
  // the same device again: the same label, so the same user on the server
  await api('POST', `/v1/vpns/${id}/join`, { member: member(1) });
  assert.equal(calls.filter((c) => c.url.endsWith('/join')).at(-1).body.label, label);
  // another device: another label
  await api('POST', `/v1/vpns/${id}/join`, { member: member(2) });
  assert.notEqual(calls.filter((c) => c.url.endsWith('/join')).at(-1).body.label, label);

  r = await api('GET', `/v1/vpns/${id}/owner`, undefined, { authorization: `Owner ${ownerKey}` });
  assert.equal(r.data.appMembers, 2);
  r = await api('GET', `/v1/vpns/${id}/owner`, undefined, { authorization: `Owner ${'0'.repeat(32)}` });
  assert.equal(r.status, 403);

  r = await api('POST', `/v1/vpns/${id}/leave`, { member: member(1) });
  assert.equal(r.status, 200);
  assert.equal(calls.at(-1).body.label, label);

  // sharing the same pool again is refused
  r = await api('POST', '/v1/vpns', { source: LINK, name: 'again' });
  assert.equal(r.status, 409);
});

test('owners edit, pause and delete; nobody else can', async () => {
  const { data } = await api('POST', '/v1/vpns', { source: 'https://panel.example.net/sub/xyz', name: 'Panel' });
  const { id, ownerKey } = data;
  assert.deepEqual(data.vpn.protocols, ['VLESS REALITY', 'Shadowsocks']);
  const owner = { authorization: `Owner ${ownerKey}` };
  let r = await api('PATCH', `/v1/vpns/${id}`, { name: 'Renamed', paused: true }, owner);
  assert.equal(r.data.vpn.name, 'Renamed');
  assert.equal((await api('GET', '/v1/vpns')).data.vpns.length, 0, 'paused: not listed');
  r = await api('POST', `/v1/vpns/${id}/join`, { member: member(9) });
  assert.equal(r.status, 409);
  await api('PATCH', `/v1/vpns/${id}`, { paused: false }, owner);
  r = await api('POST', `/v1/vpns/${id}/join`, { member: member(9) });
  assert.equal(r.data.source, 'https://panel.example.net/sub/xyz', 'a shared subscription: everyone gets the URL');
  assert.equal((await api('DELETE', `/v1/vpns/${id}`, undefined, { authorization: 'Owner ' + 'f'.repeat(32) })).status, 403);
  assert.equal((await api('DELETE', `/v1/vpns/${id}`, undefined, owner)).status, 200);
  assert.equal((await api('GET', '/v1/vpns')).data.vpns.length, 0);
});

test('health checks take a VPN off the list after 3 failures, and back', async () => {
  const { data } = await api('POST', '/v1/vpns', { source: LINK, name: 'Pool' });
  const age = () => env.DB.raw.prepare('UPDATE vpns SET checked_at = 0').run();
  poolState.down = true;
  for (let i = 0; i < 3; i++) {
    age();
    await runChecks(env);
  }
  assert.equal((await api('GET', '/v1/vpns')).data.vpns.length, 0);
  poolState.down = false;
  poolState.active = 42;
  age();
  await runChecks(env);
  const r = await api('GET', '/v1/vpns');
  assert.equal(r.data.vpns.length, 1);
  assert.equal(r.data.vpns[0].active, 42, 'the check keeps the member count current');
  assert.equal(r.data.vpns[0].id, data.id);
});

test('reports: five members saying illegal or scam unlist it', async () => {
  const { data } = await api('POST', '/v1/vpns', { source: LINK, name: 'Pool' });
  for (let i = 1; i <= 5; i++) {
    const r = await api('POST', `/v1/vpns/${data.id}/report`, { member: member(i), reason: i % 2 ? 'scam' : 'illegal' });
    assert.equal(r.status, 200);
    assert.equal((await api('GET', '/v1/vpns')).data.vpns.length, i < 5 ? 1 : 0);
  }
  const r = await api('GET', '/v1/admin/reports', undefined, { authorization: `Admin ${env.ADMIN_KEY}` });
  assert.equal(r.data.reports.reduce((a, x) => a + x.n, 0), 5);
});

test('rate limits count per address, without keeping it', async () => {
  for (let i = 0; i < 5; i++) {
    await api('POST', '/v1/vpns', { source: `vless://u${i}@h:443#x`, name: `links ${i}` });
  }
  assert.equal((await api('POST', '/v1/vpns', { source: 'vless://u9@h:443#x', name: 'six' })).status, 429);
  assert.equal((await api('POST', '/v1/vpns', { source: 'vless://u9@h:443#x', name: 'six' }, { ip: '203.0.113.9' })).status, 201);
  const who = env.DB.raw.prepare('SELECT who FROM hits').all().map((r) => r.who).join(' ');
  assert.equal(who.includes('198.51.100.1'), false);
});

test('the bot: its VPNs come in, the app\'s go out', async () => {
  const bot = { authorization: `Bot ${env.BOT_KEY}` };
  assert.equal((await api('GET', '/v1/bot/export', undefined, { authorization: 'Bot wrong-key-0123456789' })).status, 403);
  const { data } = await api('POST', '/v1/vpns', { source: LINK, name: 'From the app' });

  let r = await api('POST', '/v1/bot/import', { all: true, vpns: [
    { botId: 7, kind: 'sub', source: 'https://panel.example.net/sub/bot', name: 'From the bot', country: 'NL', protocols: ['VLESS'], status: 'ok', active: 3 },
    { botId: 8, kind: 'xtune', source: LINK, name: 'The app\'s, also in the bot', status: 'ok' },
    { botId: 9, kind: 'sub', source: 'https://x.example:12345/s', name: 'unreachable port', status: 'ok' },
  ] }, bot);
  assert.deepEqual(r.data, { added: 1, updated: 0, removed: 0 });
  r = await api('GET', '/v1/vpns');
  assert.deepEqual(r.data.vpns.map((v) => v.name).sort(), ['From the app', 'From the bot']);
  const fromBot = r.data.vpns.find((v) => v.origin === 'bot');
  r = await api('POST', `/v1/vpns/${fromBot.id}/join`, { member: member(3) });
  assert.equal(r.data.source, 'https://panel.example.net/sub/bot');

  r = await api('GET', '/v1/bot/export?since=0', undefined, bot);
  assert.deepEqual(r.data.vpns.map((v) => v.id), [data.id], 'only the app\'s VPNs go to the bot');
  assert.equal(r.data.vpns[0].source, LINK, 'with the source: the bot joins members itself');

  // the bot no longer has it: it goes from the directory too
  r = await api('POST', '/v1/bot/import', { all: true, vpns: [] }, bot);
  assert.equal(r.data.removed, 1);
  assert.deepEqual((await api('GET', '/v1/vpns')).data.vpns.map((v) => v.origin), ['app']);

  // a bot admin unlists one of the app's after a report there
  await api('POST', '/v1/bot/unlist', { id: data.id }, bot);
  assert.equal((await api('GET', '/v1/vpns')).data.vpns.length, 0);
  r = await api('GET', '/v1/bot/export?since=0', undefined, bot);
  assert.equal(r.data.vpns[0].unlisted, true);
  await api('POST', '/v1/bot/unlist', { id: data.id, delete: true }, bot);
  r = await api('GET', '/v1/bot/export?since=0', undefined, bot);
  assert.deepEqual(r.data.gone, [data.id]);
});

test('a full pool is not listed and refuses joins', async () => {
  const { data } = await api('POST', '/v1/vpns', { source: LINK, name: 'Pool' });
  env.DB.raw.prepare('UPDATE vpns SET active = 100').run();
  assert.equal((await api('GET', '/v1/vpns')).data.vpns.length, 0);
  poolState.active = 100;
  const r = await api('POST', `/v1/vpns/${data.id}/join`, { member: member(5) });
  assert.equal(r.status, 409);
  assert.equal(r.data.error, 'full');
});
