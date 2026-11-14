// Tells the CHTH hub about a release, so its bot posts the notes to the updates channel.
// Run by CI after a deploy. The same file lives in every CHTH repo that announces releases.
//
//   CHTH_HUB_URL, CHTH_RELEASE_SECRET   repository secrets; without them nothing is sent
//   RELEASE_PRODUCT    the product's id: cafe, gym, shop, chthvpnbot, xtune, …
//   RELEASE_NAME       shown for a product the hub doesn't list; RELEASE_HOME links it
//   RELEASE_VERSION    the version, or leave it empty for <package version>-<date>.<commit>,
//                      read from RELEASE_PACKAGE (default package.json)
//   RELEASE_BASE       the commit the notes start after (github.event.before)
//   RELEASE_PATHS      only commits touching these paths count (default: the whole repo);
//                      RELEASE_EXCLUDE takes paths out. CI, docs and tests never count.
//   RELEASE_CHANGELOG  a Keep a Changelog file: the version's section is used as the notes
//   RELEASE_URL        where to read more
//
// The notes are the commit subjects since RELEASE_BASE. When no commit counts (only docs, CI or
// excluded paths changed) nothing is posted. A hub that is down or refuses never fails the job.
// `--dry-run` prints the event instead of sending it.
import { execFileSync } from 'node:child_process';
import { createHmac } from 'node:crypto';
import { readFileSync } from 'node:fs';

const env = (k) => (process.env[k] ?? '').trim();
const dryRun = process.argv.includes('--dry-run');
const git = (...args) => { try { return execFileSync('git', args, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] }).trim(); } catch { return ''; } };
const words = (s) => s.split(/\s+/).filter(Boolean);
const MAX_LINES = 30;
const MAX_NOTES = 3000;

const head = git('rev-parse', 'HEAD');

function version() {
  if (env('RELEASE_VERSION')) return env('RELEASE_VERSION');
  let base = '0.0.0';
  try { base = JSON.parse(readFileSync(env('RELEASE_PACKAGE') || 'package.json', 'utf8')).version || base; } catch { /* no package.json */ }
  const day = new Date().toISOString().slice(0, 10).replaceAll('-', '');
  return `${base}-${day}.${(process.env.GITHUB_SHA || head || 'unknown').slice(0, 7)}`;
}

/** The commit the notes start after: the push's previous head, the previous tag, or the parent. */
function baseCommit() {
  const before = env('RELEASE_BASE');
  if (/^[0-9a-f]{40}$/.test(before) && !/^0+$/.test(before) && git('cat-file', '-t', before) === 'commit') return before;
  if ((process.env.GITHUB_REF ?? '').startsWith('refs/tags/')) {
    const previous = git('describe', '--tags', '--abbrev=0', 'HEAD^');
    if (previous) return previous;
  }
  return git('rev-parse', '--verify', '--quiet', 'HEAD~1');
}

function commitNotes() {
  const base = baseCommit();
  const paths = [
    ...(words(env('RELEASE_PATHS')).length ? words(env('RELEASE_PATHS')) : ['.']),
    ...words(env('RELEASE_EXCLUDE')).map((p) => `:(exclude)${p}`),
    ':(exclude).github', ':(exclude)*.md', ':(exclude,glob)**/docs/**', ':(exclude,glob)**/test/**', ':(exclude,glob)**/tests/**',
  ];
  const log = git('log', '--no-merges', '--reverse', '--format=%s', base ? `${base}..HEAD` : '-1', '--', ...paths);
  const subjects = [...new Set(log.split('\n').map((s) => s.replace(/\s*\(#\d+\)$/, '').trim()).filter(Boolean))];
  const lines = subjects.slice(0, MAX_LINES).map((s) => `• ${s}`);
  if (subjects.length > MAX_LINES) lines.push(`…and ${subjects.length - MAX_LINES} more`);
  return lines.join('\n');
}

/** The section of a Keep a Changelog file for `v`, as plain text. */
export function changelogNotes(text, v) {
  const wanted = v.replace(/^v/, '');
  const out = [];
  let inside = false;
  for (const line of text.split('\n')) {
    const h = /^## \[?([^\]\s]+)\]?/.exec(line);
    if (h) { if (inside) break; inside = h[1].replace(/^v/, '') === wanted; continue; }
    if (!inside) continue;
    const plain = line.replace(/\s*\[[^\]]*→\]\([^)]*\)/g, '').replace(/\[([^\]]+)\]\([^)]*\)/g, '$1').replace(/`/g, '');
    if (/^### /.test(plain)) out.push('', `${plain.slice(4).trim()}:`);
    else if (/^[-*] /.test(plain)) out.push(`• ${plain.slice(2).trim()}`);
    else if (plain.trim() && out.length) out[out.length - 1] += ` ${plain.trim()}`; // a wrapped line
  }
  return out.join('\n').trim();
}

function notes(v) {
  if (env('RELEASE_CHANGELOG')) {
    try {
      const section = changelogNotes(readFileSync(env('RELEASE_CHANGELOG'), 'utf8'), v);
      if (section) return section;
    } catch { /* no changelog: the commits will do */ }
  }
  return commitNotes();
}

/** The body of the ingest request. The hub takes at most 4096 characters of data an event. */
export function payload({ product, name, home, version: v, text, url }) {
  let body = text.length > MAX_NOTES ? `${text.slice(0, MAX_NOTES).replace(/\n[^\n]*$/, '')}\n…` : text;
  for (;;) {
    const data = { product, version: v, notes: body, ...(name ? { name } : {}), ...(home ? { home } : {}), ...(url ? { url } : {}) };
    if (JSON.stringify(data).length <= 3900) return JSON.stringify({ events: [{ type: 'release', at: Math.floor(Date.now() / 1000), data }] });
    body = `${body.slice(0, Math.max(0, body.length - 300)).replace(/\n[^\n]*$/, '')}\n…`;
  }
}

export const sign = (secret, ts, path, body) => createHmac('sha256', secret).update(`${ts}.POST ${path}.${body}`).digest('hex');

async function main() {
  const product = env('RELEASE_PRODUCT');
  if (!product) throw new Error('Set RELEASE_PRODUCT.');
  const v = version();
  const text = notes(v);
  if (!text) return console.log(`Nothing to announce for ${product} ${v}: no commit that counts.`);
  const body = payload({ product, name: env('RELEASE_NAME'), home: env('RELEASE_HOME'), version: v, text, url: env('RELEASE_URL') });
  if (dryRun) return console.log(JSON.stringify(JSON.parse(body), null, 2));
  const hub = env('CHTH_HUB_URL').replace(/\/$/, '');
  const secret = env('CHTH_RELEASE_SECRET');
  if (!hub || !secret) return console.log('::warning::CHTH_HUB_URL or CHTH_RELEASE_SECRET is not set. Skipping the release post.');
  const url = new URL(`${hub}/ingest`);
  const ts = String(Math.floor(Date.now() / 1000));
  try {
    const res = await fetch(url, {
      method: 'POST', body, signal: AbortSignal.timeout(20_000),
      headers: { 'Content-Type': 'application/json', 'X-Chth-Source': 'releases', 'X-Chth-Timestamp': ts, 'X-Chth-Signature': sign(secret, ts, url.pathname, body) },
    });
    if (res.ok) console.log(`Announced ${product} ${v}.`);
    else console.log(`::warning::The hub refused the release post (HTTP ${res.status}).`);
  } catch (e) {
    console.log(`::warning::The release post did not reach the hub: ${e?.message ?? e}`);
  }
}

if (process.argv[1] && import.meta.url === new URL(`file://${process.argv[1]}`).href) await main();
