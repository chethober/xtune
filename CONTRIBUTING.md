# Contributing

Thanks for helping! Issues and pull requests are welcome.

## Development

```sh
go build -o xtune .
go vet ./... && go test ./...
gofmt -l .        # must print nothing
```

CI runs vet, tests and builds on Linux, macOS and Windows, checks `gofmt`, runs `shellcheck` on `helper.sh` and
`install.sh`, and cross-builds every release target.

## Guidelines

- Keep the single static binary: no cgo, no runtime dependencies on the user's machine.
- Anything that runs on the server goes through `helper.sh` (embedded) and must stay inside `/opt/xray-autotune`
  or the `xray-autotune*` units. Never modify other software on the server.
- Secrets are never written to argv or to local files; profiles store connection details only.
- When you add a protocol or knob, add it to the test matrix *and* the share-link generator, with a unit test for the link.
- Test changes against a real server before opening a PR, and say in the PR what you tested.

## Releases

Maintainers tag `vX.Y.Z` on `main` and the release workflow publishes binaries with GoReleaser.
Update `CHANGELOG.md` in the same PR as the change.
