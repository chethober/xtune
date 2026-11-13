# Contributing

Thanks for helping! Issues and pull requests are welcome.

## Development

```sh
go build -o xtune .
go vet ./... && go test ./...
gofmt -l .        # must print nothing
XTUNE_E2E=1 go test -run EndToEnd ./...   # optional: real local Xray, needs a local xray and internet access
```

Golden files: `go test -run Compat . -args -update` after a deliberate change to `xtune help`, and
`go test ./pkg/state -run TestCurrentFixture -args -update` after adding fields to `state.json` or `usage.json`.

CI runs vet, tests and builds on Linux, macOS and Windows, checks `gofmt`, runs `shellcheck` on `pkg/remote/helper.sh` and
`install.sh`, and cross-builds every release target (Linux, macOS, Windows on amd64/arm64, and Android arm64).

## Guidelines

- Keep the single static binary: no cgo, no runtime dependencies on the user's machine.
- Anything that runs on the server goes through `pkg/remote/helper.sh` (embedded) and must stay inside `/opt/xray-autotune`,
  the `xray-autotune*` units (Xray, test, enforce timer, subscription server) and the `/usr/local/bin/xtune` link. Never modify other software on the server.
- The same binary is the client and the server tool. Code that changes the server takes a `*remote.Server` (`pkg/remote`), so it works
  over SSH and locally; user management stays server-side only.
- `pkg/state` holds the data model (`state.json`, `usage.json`) and nothing that builds configs or changes the
  server; a field added there must keep both files readable by older releases (`omitempty`, or `json:"-"` for
  in-memory-only fields).
- Secrets are never written to argv or to local files; profiles store connection details only. The one exception
  is a backup the user asks for (`xtune backup`), written readable by the owner only.
- Commands, flags, `--json` keys, the state files and users' links are stable within 1.x
  ([docs/compatibility.md](docs/compatibility.md)): add, do not rename or remove. Renaming a state field needs a
  migration in `pkg/state/schema.go` and a `Schema` bump.
- Document user-facing changes in `docs/` (the README only summarizes).
- When you add a protocol or knob, add it to the test matrix *and* the share-link generator, with a unit test for the link.
- Test changes against a real server before opening a PR, and say in the PR what you tested.

## Releases

Maintainers tag `vX.Y.Z` on `main` and the release workflow publishes binaries with GoReleaser.
Update `CHANGELOG.md` in the same PR as the change.
