# Contributing

## Setup

The toolchain is pinned with [mise](https://mise.jdx.dev/):

```sh
mise trust && mise install
mise run test       # go test -race ./...
mise run lint       # golangci-lint
mise run build      # static linux/riscv64 binary in dist/
mise run sizecheck  # fails if the binary exceeds 15 MB
mise run apicheck   # fails if upstream routes or payload shapes we depend on drifted
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

  Speaking the envelope is not enough on its own. #29 and both bugs in #31 had
  green tests over fakes that agreed fluently with *our* encoder, so our side
  and the fake were wrong together. Two things close that:

  1. A fake's behaviour must be traceable to a specific piece of upstream
     source. Name the file in a comment, as `pasteHandler` in
     `internal/nanokvm/hid_test.go` does.
  2. Where a fake decides whether input is well-formed, it applies upstream's
     own acceptance rule and answers the way upstream answers — including
     rejections, which the firmware returns as a non-zero envelope code on
     HTTP 200, not as a 4xx. `gpioHandler` in `internal/nanokvm/vm_test.go`
     rejects an unknown power event with code `-2` because upstream's
     `SetGpio` does.

- **Every route we call must be visible to `tools/apicheck`, in both of its
  files.** `routes.txt` proves the route still exists upstream; `shapes.txt`
  records what travels over it, pairing the route with upstream's declared type
  and ours. A route with nothing to diff still needs a row saying so and why,
  so that an omission reads as a decision. Add new firmware calls as client
  methods in `internal/nanokvm`, not as raw `Do(...)` calls scattered
  elsewhere, and give request and response bodies named types — an inline
  `map[string]any` literal is nothing the differ can hold against upstream.

  `apicheck` reads upstream's struct declarations, so it sees payload shapes
  and nothing else. Framing and transport semantics are declared as types
  nowhere: the `/api/ws` HID byte protocol and the fact that
  `/api/stream/mjpeg` never sends EOF are guarded only by the fakes rule above.
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
