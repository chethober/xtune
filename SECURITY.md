# Security policy

## Reporting a vulnerability

Please **do not open a public issue** for security problems. Use GitHub's
[private vulnerability reporting](https://github.com/chethober/xtune/security/advisories/new) instead.
Include the version (`xtune version`), what you found, and how to reproduce it. You should get a reply within a week.

## Scope

In scope: anything that could expose credentials (SSH, sudo, panel tokens, Xray keys), let a third party reach
services on a managed server, or make xtune change the server beyond what the README describes.

## How secrets are handled

- SSH and sudo passwords and panel credentials are kept in memory only. They are sent over the SSH channel on stdin,
  never on a command line, and never written to disk. Saved profiles hold host, port, user and key path only.
- Host keys are pinned in `~/.ssh/known_hosts` on first use, and a changed key aborts the connection.
- Server state (`/opt/xray-autotune/state.json`, keys, certificates) is root-only (0600, in a 0700 directory).
- Local result files (links and client configs contain credentials) are written with mode 0600.
- Xray downloads are verified against the release's SHA2-256 digest.
