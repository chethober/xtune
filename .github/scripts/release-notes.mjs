// Writes a release's notes to stdout: the CLI's section of cli/CHANGELOG.md for the version, and
// the app's notes (app/RELEASE_NOTES.md) when they changed since the previous release.
//   node .github/scripts/release-notes.mjs v1.5.0
import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';

const version = (process.argv[2] ?? '').replace(/^v/, '');
if (!/^\d+\.\d+\.\d+/.test(version)) throw new Error('Usage: release-notes.mjs vX.Y.Z');
const git = (...args) => { try { return execFileSync('git', args, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] }).trim(); } catch { return null; } };

/** The body of the `## [version]` section of a Keep a Changelog file. */
export function section(changelog, v) {
  const out = [];
  let inside = false;
  for (const line of changelog.split('\n')) {
    const h = /^## \[?v?([^\]\s]+)\]?/.exec(line);
    if (h) { if (inside) break; inside = h[1] === v; continue; }
    if (inside) out.push(line);
  }
  return out.join('\n').trim();
}

const parts = [];
const cli = section(readFileSync('cli/CHANGELOG.md', 'utf8'), version);
if (cli) parts.push(`## CLI\n\n${cli}`);
// The previous release: the nearest tag before this one (or before HEAD, when the tag is new).
const previous = git('describe', '--tags', '--abbrev=0', '--match', 'v[0-9]*', `v${version}^`) ?? git('describe', '--tags', '--abbrev=0', '--match', 'v[0-9]*', 'HEAD^');
const appChanged = !previous || git('diff', '--quiet', previous, 'HEAD', '--', 'app/RELEASE_NOTES.md') === null;
if (appChanged) {
  const app = readFileSync('app/RELEASE_NOTES.md', 'utf8').trim();
  if (app) parts.push(`## App\n\n${app}`);
}
process.stdout.write(`${parts.join('\n\n') || 'Maintenance release.'}\n`);
