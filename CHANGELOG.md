# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[semver](https://semver.org) (`vMAJOR.MINOR.PATCH` git tags).

## [Unreleased]

Initial release line. Everything below is part of the first public version.

### Added

- MCP server (streamable HTTP, bearer auth) running on the NanoKVM itself as a
  static riscv64 binary, exposing 14 tools: screenshot, LED/HDMI status, device
  info/hardware, ISO list/mount/unmount, batched HID input, power/power-cycle,
  HDMI reset, and HID reset.
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
