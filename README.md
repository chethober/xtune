# xtune

[![ci](https://github.com/chethober/xtune/actions/workflows/ci.yml/badge.svg)](https://github.com/chethober/xtune/actions/workflows/ci.yml)
[![app](https://github.com/chethober/xtune/actions/workflows/app-ci.yml/badge.svg)](https://github.com/chethober/xtune/actions/workflows/app-ci.yml)
[![release](https://img.shields.io/github/v/release/chethober/xtune?sort=semver)](https://github.com/chethober/xtune/releases)
[![license](https://img.shields.io/github/license/chethober/xtune)](LICENSE)

**Find the Xray configs that actually get through *your* network, deploy them, and manage users on the server.**

| | |
|---|---|
| [`cli/`](cli/README.md) | **xtune**, the command-line tool: it tunes, deploys and manages a server over SSH, from one binary |
| [`app/`](app/README.md) | **xtune client**, the app for Android, Linux and Windows: connect, check configs, manage the server |

The two share a version. Each [release](https://github.com/chethober/xtune/releases) has the CLI's binaries and the
app's builds.

## Install the CLI

```sh
# Linux / macOS / Android (Termux)
curl -fsSL https://raw.githubusercontent.com/chethober/xtune/main/install.sh | sh
```

```powershell
# Windows (PowerShell)
irm https://raw.githubusercontent.com/chethober/xtune/main/install.ps1 | iex
```

More in [cli/README.md](cli/README.md).

## Install the app

Download the file for your device from [Releases](https://github.com/chethober/xtune/releases): an
`android-…apk`, `linux-x64.tar.gz` or `windows-x64.zip`. More in [app/README.md](app/README.md).

## Releasing

Push a tag like `v1.5.0` (or run the `release` workflow with a version). The release's notes are that version's
section of [cli/CHANGELOG.md](cli/CHANGELOG.md) and [app/RELEASE_NOTES.md](app/RELEASE_NOTES.md).

## License

[MIT](LICENSE). To report a vulnerability, see [SECURITY.md](SECURITY.md).
