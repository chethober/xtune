# Contributing

Thanks for helping! Issues and pull requests are welcome.

## Development

```sh
go build -o xtune .
go vet ./... && go test ./...
gofmt -l .        # must print nothing
```

CI runs vet, tests and builds on Linux, macOS and Windows, checks `gofmt`, runs `shellcheck` on `helper.sh` and
`install.sh`, and cross-builds every release target (Linux, macOS, Windows on amd64/arm64, and Android arm64).

## Guidelines

- Keep the single static binary: no cgo, no runtime dependencies on the user's machine.
- Anything that runs on the server goes through `helper.sh` (embedded) and must stay inside `/opt/xray-autotune`,
  the `xray-autotune*` units (Xray, test, enforce timer, subscription server) and the `/usr/local/bin/xtune` link. Never modify other software on the server.
- The same binary is the client and the server tool. Code that changes the server takes a `*Server`, so it works
  over SSH and locally; user management stays server-side only.
- Secrets are never written to argv or to local files; profiles store connection details only.
- When you add a protocol or knob, add it to the test matrix *and* the share-link generator, with a unit test for the link.
- Test changes against a real server before opening a PR, and say in the PR what you tested.

## Releases

Maintainers tag `vX.Y.Z` on `main` and the release workflow publishes binaries with GoReleaser.
Update `CHANGELOG.md` in the same PR as the change.
