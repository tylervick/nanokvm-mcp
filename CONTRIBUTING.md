# Contributing

## Setup

The toolchain is pinned with [mise](https://mise.jdx.dev/):

```sh
mise trust && mise install
mise run test       # go test -race ./...
mise run lint       # golangci-lint
mise run build      # static linux/riscv64 binary in dist/
mise run sizecheck  # fails if the binary exceeds 15 MB
mise run apicheck   # fails if upstream routes we depend on drifted
```

All five must pass before a PR. CI runs exactly these.

## Constraints that shape this codebase

- **The device has ~43 MB of free RAM.** The binary budget is 15 MB on disk and
  ~25 MB resident. Anything that decodes images or buffers streams must be
  bounded (see `internal/backend/public.go` for the pattern). If your change
  grows the binary or the heap, say so in the PR.
- **No mocked HTTP/WebSocket transports in tests.** Tests run against
  `httptest` fakes that speak the real firmware envelope and status codes.
  This rule exists because the predecessor project had 107 green tests over a
  broken product — the mocks validated themselves.
- **Every route we call must be visible to `tools/apicheck`.** Add new firmware
  calls as client methods in `internal/nanokvm`, not as raw `Do(...)` calls
  scattered elsewhere.
- **Write the test first.** Bug fixes need a test that reproduces the bug and
  fails before the fix.

## Licensing

GPL-3.0, matching upstream [`sipeed/NanoKVM`](https://github.com/sipeed/NanoKVM).
If you port logic from upstream (keycode tables, action semantics, wire
formats), keep a file header noting the origin, as in `internal/nanokvm/auth.go`.

## Releases

Annotated semver tags from a clean `main` checkout:

```sh
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

The tag push triggers `.github/workflows/release.yml`, which runs GoReleaser
to build the static riscv64 binary and publish the GitHub release with
checksums — no manual artifact uploads. Update `CHANGELOG.md` (move
`[Unreleased]` items under the new version) in the same commit that gets
tagged. Dry-run locally with
`mise x goreleaser -- goreleaser release --snapshot --clean`.
