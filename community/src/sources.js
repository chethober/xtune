// What can be shared, recognised from what the owner sends: an xtune connect link, a
// subscription URL, or share links. The same rules as @chthvpnbot's (bots/chthvpnbot/tgcloud/lib/sources.js),
// so one VPN can be listed in both. No Worker APIs here: tested with plain node.

// xtune's connect link: https://HOST[:PORT]/<sub path>/pub/<32 hex token>
const XTUNE_RE = /^https:\/\/[^\s/]+\/[^\s/]+\/pub\/[0-9a-f]{32}$/;
const URL_RE = /^https?:\/\/[^\s]+$/;
const SCHEMES = ['vless', 'vmess', 'trojan', 'ss', 'ssr', 'hysteria2', 'hy2', 'hysteria', 'tuic', 'wireguard', 'wg', 'ssh', 'socks'];
const LINK_RE = new RegExp(`^(${SCHEMES.join('|')})://\\S+$`, 'i');

export const MAX_LINKS = 20;
export const MAX_LINKS_CHARS = 3500;
export const NAME_MAX = 40;
export const DESC_MAX = 300;

// Workers reach other servers on these ports only (Cloudflare's proxied ports).
export const PORTS = [80, 443, 2052, 2053, 2082, 2083, 2086, 2087, 2095, 2096, 8080, 8443, 8880];

export function portOf(url) {
  const m = /^(https?):\/\/(?:\[[^\]]+\]|[^/:\s]+)(?::(\d+))?(?:\/|$)/.exec(url);
  if (!m) return null;
  return m[2] ? Number(m[2]) : m[1] === 'https' ? 443 : 80;
}

// → { kind: 'xtune' | 'sub' | 'links', source } | { error: 'empty' | 'tooMany' | 'tooLong' | 'unknown' | 'port' }
export function detectSource(text) {
  const t = String(text ?? '').trim();
  if (!t) return { error: 'empty' };
  if (/^https?:\/\/\S+$/.test(t)) {
    const p = portOf(t);
    if (p !== null && !PORTS.includes(p)) return { error: 'port', port: p };
  }
  if (XTUNE_RE.test(t)) return { kind: 'xtune', source: t };
  if (URL_RE.test(t) && !t.includes('\n')) return { kind: 'sub', source: t };
  const lines = t.split(/\s*\n\s*/).filter(Boolean);
  if (!lines.every((l) => LINK_RE.test(l))) return { error: 'unknown' };
  if (lines.length > MAX_LINKS) return { error: 'tooMany' };
  const joined = lines.join('\n');
  if (joined.length > MAX_LINKS_CHARS) return { error: 'tooLong' };
  return { kind: 'links', source: joined };
}

const PROTO_NAMES = {
  vless: 'VLESS', vmess: 'VMess', trojan: 'Trojan', ss: 'Shadowsocks', ssr: 'ShadowsocksR',
  hysteria2: 'Hysteria2', hy2: 'Hysteria2', hysteria: 'Hysteria', tuic: 'TUIC', wireguard: 'WireGuard',
  wg: 'WireGuard', ssh: 'SSH', socks: 'SOCKS',
};

// Protocol names in the order they first appear. VLESS links say whether they are REALITY.
export function protocolsOf(links) {
  const out = [];
  for (const l of String(links).split('\n')) {
    const m = /^([a-z0-9]+):\/\//i.exec(l.trim());
    if (!m) continue;
    let name = PROTO_NAMES[m[1].toLowerCase()];
    if (!name) continue;
    if (name === 'VLESS' && /[?&]security=reality/i.test(l)) name = 'VLESS REALITY';
    if (!out.includes(name)) out.push(name);
  }
  return out;
}

// The share links in a fetched subscription body: base64 (the usual) or plain lines.
export function parseSubscription(body) {
  const text = String(body ?? '').trim();
  let decoded = text;
  if (!/:\/\//.test(text)) {
    try {
      const b = text.replace(/\s+/g, '').replace(/-/g, '+').replace(/_/g, '/');
      decoded = new TextDecoder().decode(Uint8Array.from(atob(b + '='.repeat((4 - (b.length % 4)) % 4)), (c) => c.charCodeAt(0)));
    } catch {
      return [];
    }
  }
  return decoded.split(/\r?\n/).map((l) => l.trim()).filter((l) => LINK_RE.test(l));
}

export const COUNTRY_RE = /^[A-Z]{2}$/;

// Cleans what an owner typed: one line, at most n characters.
export function clean(s, n) {
  return String(s ?? '').replace(/[\u0000-\u001f\u007f]+/g, ' ').replace(/\s+/g, ' ').trim().slice(0, n);
}

// A member's label on an xtune server (the part of the user name after POOL-): 10 characters
// from a hash of the device's member key and the VPN, so the same device gets the same user
// back, and the server learns nothing about who it is.
export const LABEL_ALPHABET = 'abcdefghijkmnpqrstuvwxyz23456789';
export function labelFrom(hashHex) {
  let out = '';
  for (let i = 0; i < 10; i++) out += LABEL_ALPHABET[parseInt(hashHex.slice(i * 2, i * 2 + 2), 16) % LABEL_ALPHABET.length];
  return out;
}
