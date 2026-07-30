# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[semver](https://semver.org) (`vMAJOR.MINOR.PATCH` git tags).

## [Unreleased]

## [0.1.1] - 2026-07-30

Bugfix release: a client used concurrently from cold now opens one firmware
session instead of one per caller. Everything else is documentation.

### Fixed

- Concurrent first use of a `nanokvm.Client` no longer opens a firmware session
  per caller. `Token()` sampled the token, released the lock, and only then
  logged in, so goroutines arriving together each ran a full login; login is
  now serialized behind its own mutex with a token re-check, and callers queued
  behind an in-flight login reuse its result (#10).

### Documentation

- Day-to-day Claude Code setup, as validated on hardware: an SSH master socket
  carrying the tunnel (one auth), the dropbear agent-flood gotcha,
  `ExitOnForwardFailure` and tunnel liveness checks, and the bearer token
  pulled to a mode-600 file and used by reference everywhere (#9).
- Capture trails HID input by roughly one frame on firmware 2.4.3, so a
  screenshot taken immediately after input can come back showing the screen
  before the input landed. Workaround: end the input batch with a `wait` action
  of ~100–200 ms, or take a second screenshot (#11).
- CI, release, and license badges at the top of the README (#12).

## [0.1.0] - 2026-07-28

Initial public release, validated on real hardware (NanoKVM Beta, firmware
2.4.3): full read/write loop over MCP at ~8.4 MB RSS.

### Added

- MCP server (streamable HTTP, bearer auth) running on the NanoKVM itself as a
  static riscv64 binary, exposing 14 tools: screenshot, LED/HDMI status, device
  info/hardware, ISO list/mounted-image query/mount/unmount, batched HID input,
  power/power-cycle, HDMI reset, and HID reset.
- Two capture/input backends: `picoclaw` (firmware-internal API, raw JPEG
  passthrough, preferred) and `public` (MJPEG + WebSocket fallback with a hard
  2.1 Mpx decode cap), selected once at startup.
- Read-only mode (`NANOKVM_MCP_READONLY=true`) that unregisters the 7 mutating
  tools entirely.
- Audit log of mutating calls with typed text redacted by default.
- `tools/apicheck` drift guard failing CI when an upstream route we depend on
  disappears.
- Deploy tooling: `deploy/install.sh` and the `S96nanokvm-mcp` init script
  (busybox-compatible, logs to `/data/nanokvm-mcp/daemon.log`).
- CI: `-race` tests, golangci-lint, 15 MB binary size gate, apicheck.

### Notes

- Validated on real hardware (NanoKVM Beta, firmware 2.4.3): full read/write
  loop over MCP, ~8.4 MB RSS.
