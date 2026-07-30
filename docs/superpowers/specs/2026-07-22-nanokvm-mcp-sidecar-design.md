# NanoKVM MCP Sidecar — Design

**Date:** 2026-07-22
**Status:** Approved for planning

## Summary

An MCP server for Sipeed NanoKVM, written in Go and running **on the device itself**
as a standalone daemon alongside the stock firmware. It exposes power control, HID
input, screen capture, and ISO mounting to MCP clients over authenticated HTTP.

This replaces the approach of forking
[`scgreenhalgh/nanokvm-mcp`](https://github.com/scgreenhalgh/nanokvm-mcp) (Python,
off-device), which was evaluated and rejected — see Background.

## Device recon (verified 2026-07-23)

Run against a real NanoKVM (firmware 2.4.3, `beta` hardware) via
`scripts/device-check.sh`. Results that shape the design:

- **Arch:** `riscv64` GNU/Linux, musl (`ld-musl-riscv64`). Static `CGO_ENABLED=0`
  riscv64 binary built here runs on-device. Build assumption confirmed.
- **Memory:** **128 MB total, 43 MB available** — not the ~98 MB inferred from the
  SG2002 datasheet. The 25 MB resident budget fits, but headroom is ~18 MB. This makes
  the JPEG-decode discipline a hard safety requirement, not a precaution (see Memory
  strategy).
- **Filesystems:** `/` is ext4 (7.6 GB, `mmcblk0p2`); `/data` is a dedicated **21.5 GB
  exfat** partition (`mmcblk0p3`); `/usr/local/bin` does not exist. `/kvmapp`, `/root`,
  `/etc/init.d`, and `/data` are all writable.
- **Install path (decided):** binary + config in **`/root/nanokvm-mcp/`** (ext4, real
  Unix permission/exec bits; updates wipe only `/root/old` and `/root/.kvmcache`, never
  `/root` itself); **audit log in `/data/nanokvm-mcp/`** (space, spares rootfs write
  wear, and survives a rootfs reflash as a separate partition). Init script
  `/etc/init.d/S96nanokvm-mcp` (the `S95nanokvm` slot precedes it; `S96` is free).
- **PicoClaw internal path works:** `/etc/kvm/.picoclaw_internal_token` exists (88 chars);
  `GET /api/picoclaw/screenshot` and `POST /api/picoclaw/actions` both return 200 with the
  token + an arbitrary `X-PicoClaw-Session-ID`. `picoclawBackend` is viable with no
  runtime install.
- **`/api/vm/gpio/led` returns 404 on live firmware** — the Python fork's LED bug,
  confirmed against hardware.
- **`beta` hardware has no HDD LED.** `gpio.go` hardcodes `hdd=false` unless the board is
  `HWVersionAlpha`. `nanokvm_led_status` must present `hdd` as possibly-unavailable rather
  than a real reading.

Resolved constants (from source, no device needed): `sessionIDHeader =
"X-PicoClaw-Session-ID"`; `AppDir = /kvmapp`, `BackupDir = /root/old`, `CacheDir =
/root/.kvmcache`.

## Background

### Why not the existing Python fork

The fork was audited in full (1,285 lines of source, 1,544 of tests). It contains no
malicious code and its 5 dependencies are all legitimate. It is, however, broken on
current dependencies and structurally prone to staying broken:

1. **All WebSocket HID is broken.** `pyproject.toml` pins `websockets>=12.0`, which
   resolves to 16.x, where `ClientConnection` has no `.closed` attribute. `client.py:270`
   checks it. Reproduced against a live local server: the first `_send_ws` succeeds and
   the second raises `AttributeError`. Since `send_key()` sends key-down then key-up as
   two messages, **the first keypress leaves a key physically held down** on the target.
   Same for `mouse_click` (button stuck down).
2. **`nanokvm_led_status` 404s.** It calls `GET /api/vm/gpio/led`; upstream serves
   `GET /api/vm/gpio` returning `{pwr, hdd}`.
3. **The tests do not protect against either.** All 107 pass, because `conftest.py`
   mocks the WebSocket with a `MagicMock` whose `.closed` returns a truthy Mock. The
   suite validates the mocks, not the code.
4. **No annotations.** 0 of 19 tools declare `readOnlyHint`/`destructiveHint`, so a
   client cannot distinguish "read the power LED" from "force-shutdown the server".
5. No CI, no lockfile, no LICENSE file, and `.python-version` contains the string
   `nanokvm-mcp` — a leaked local pyenv artifact.

The root cause of (2) is architectural: the client was written against a hand-copied
`API_REFERENCE.md` snapshot that drifts from a live upstream. Any off-device port
inherits that failure mode.

### What upstream actually provides

`sipeed/NanoKVM` is GPL-3.0, 6,399 stars, and actively developed (pushed 2026-07-22).
The Go server is fully open: `server/router/*.go` (endpoints), `server/proto/*.go`
(request/response structs), `server/service/hid/*` (HID protocol),
`server/service/auth/password.go` (the CryptoJS-compatible encryption). Reverse-engineered
protocol knowledge is therefore **not** a moat, and `API_REFERENCE.md` has no
independent value.

Upstream also ships its own MCP server at `server/service/picoclaw/mcp_handler.go`, but
it is gated on `isLoopbackRemote(RemoteAddr) && hasValidLoopbackHTTPToken(req)` —
loopback only. It serves Sipeed's on-device agent ("PicoClaw"), which relays screenshots
to a cloud model gateway. It is not reachable from a laptop, declares
`protocolVersion: "2024-11-05"` (two revisions stale), exposes 2 tools, and has no
notification handling. **We do not reuse this layer.** We reuse the services beneath it.

### Why on-device

- **It structurally solves the staleness problem.** A sidecar compiled and tested against
  upstream's own route definitions cannot silently drift the way a hand-copied snapshot does.
- **The KVM's purpose is being reachable when the target is down.** An MCP server that
  only works while a laptop is awake undercuts that.
- Upstream did on-device MCP themselves, which is evidence the pattern fits the device.

### Why Go

Forced by the hardware. The common SG2002 NanoKVM is a single 1.0 GHz C906 RISC-V core
with 256 MB DDR3, of which upstream's README states 158 MB is allocated to the multimedia
subsystem — leaving roughly 98 MB on paper (43 MB actually free on the test board once the
firmware is running). Python is not viable: the `mcp`
SDK pulls `pydantic-core` (Rust) and image handling pulls Pillow, both requiring
riscv64-musl cross-builds, for a runtime that would contend with live video encoding.

Go cross-compiles to `linux/riscv64` with `CGO_ENABLED=0` and needs no custom toolchain,
no `patchelf`, and no access to the proprietary `dl_lib/*.so` blobs — because the sidecar
never touches the media pipeline, only the HTTP API in front of it.

## Licensing

**GPL-3.0**, matching upstream.

This is chosen deliberately rather than accepted reluctantly: it grants us freedom to
port code from `sipeed/NanoKVM` (keycode maps, action-execution logic) without any
derivative-work analysis. Under an HTTP-only boundary we would not be *required* to be
GPL, but electing it up front removes a class of question from every future decision and
may reduce implementation work.

A `LICENSE` file (GPL-3.0 full text) ships in the repo root. Files containing ported
upstream code carry a header noting origin.

## Architecture

```
Laptop — Claude Code / Claude Desktop
   │
   │  MCP streamable HTTP + bearer token
   │  (over an SSH port-forward to loopback)
   ▼
NanoKVM device (riscv64 / SG2002)
┌─────────────────────────────────────────────┐
│  nanokvm-mcp  (this project)                │
│    persistent path + /etc/init.d/S96…       │
│                                             │
│    mcp: tools, annotations, read-only gate  │
│    audit: mutating-call log                 │
│                                             │
│    backend.KVMBackend ─┬─ picoclawBackend   │
│                        └─ publicBackend     │
└────────────────────────┼────────────────────┘
                         │ HTTP to 127.0.0.1
                         ▼
              NanoKVM-Server (gin, stock firmware)
```

The sidecar is a pure HTTP client of the stock server. It does not link against it, does
not modify `AppDir`, and does not require the PicoClaw runtime to be installed.

## Components

| Package | Responsibility |
|---|---|
| `cmd/nanokvm-mcp` | Config, wiring, serve. No logic. |
| `internal/mcp` | Tool definitions, annotations, registration, read-only filter |
| `internal/backend` | `KVMBackend` interface + `picoclaw` and `public` implementations + selection |
| `internal/nanokvm` | REST client for the public API (JWT auth, vm, storage, hid, stream) |
| `internal/hid` | Keycode tables and HID report construction |
| `internal/audit` | Mutating-call log |
| `tools/apicheck` | Upstream route drift detector |
| `deploy` | Init script, install script |

Each is independently testable and depends only on the layer below it.

### `backend.KVMBackend`

Screen capture and HID input are the only capabilities where upstream's internal API
offers something the public API does not, so they sit behind an interface:

```go
type KVMBackend interface {
    Screenshot(ctx context.Context, opts ScreenshotOpts) ([]byte, Meta, error)
    Input(ctx context.Context, actions []Action) (ActionResult, error)
    Name() string
}
```

**`picoclawBackend`** (preferred) calls `GET /api/picoclaw/screenshot` and
`POST /api/picoclaw/actions`, sending `X-NanoKVM-Internal-Token` read from
`/etc/kvm/.picoclaw_internal_token` (mode 0600) plus a session-ID header. It provides:

- on-device downscale and JPEG re-encode, so full-resolution frames never cross the network
- batched actions in one round-trip — material on a 1 GHz core
- **normalized `[0,1]` coordinates**, which are resolution-independent and remove the
  `SCREEN_WIDTH`/`SCREEN_HEIGHT` configuration the Python version required
- direct HID gadget writes (`WriteHid0`/`WriteHid2`), bypassing the WebSocket entirely —
  the entire failure class currently breaking the Python server does not exist here

`SessionLock.acquire` rejects only the empty session ID, so any non-empty value takes the
lock; the PicoClaw runtime need not be installed.

**`publicBackend`** (fallback) drives `POST /api/hid/paste`, `/api/ws`, and
`GET /api/stream/mjpeg`, doing coordinate mapping and image resize in-process. Under
GPL-3.0 its input logic may be ported from `server/service/picoclaw/actions.go` rather
than reimplemented.

**Selection is made once at startup**, probing for the token file and issuing one
harmless request. The chosen backend is logged. There is **no mid-flight fallback**: if
`picoclawBackend.Input` fails after dispatching some actions, retrying on another backend
could double-execute them. On a 401/404 the sidecar marks the backend stale and re-probes
before the *next* call only.

All other capabilities — power, LED, HDMI, storage, info — have no internal-API
equivalent and always use the public REST API, which the web UI depends on and is
therefore de-facto stable.

## Tool surface

14 tools. Input is deliberately batched into one tool; everything else is narrow, so
annotations give the client meaningful approval granularity.

**Read-only** (`readOnlyHint: true`)

| Tool | Source |
|---|---|
| `nanokvm_screenshot` | backend |
| `nanokvm_led_status` | `GET /api/vm/gpio` → `{pwr, hdd}`; `hdd` is always `false` on non-`alpha` hardware and is reported as unavailable there |
| `nanokvm_hdmi_status` | `GET /api/vm/hdmi` |
| `nanokvm_list_images` | `GET /api/storage/image` |
| `nanokvm_mounted_image` | `GET /api/storage/image/mounted` |
| `nanokvm_info` | `GET /api/vm/info` |
| `nanokvm_hardware` | `GET /api/vm/hardware` |

**Mutating**

| Tool | Annotations | Source |
|---|---|---|
| `nanokvm_input` | `destructive`, not idempotent | backend — batched click/move/type/hotkey/scroll/drag/wait, normalized coords |
| `nanokvm_power` | `destructive` | `POST /api/vm/gpio` |
| `nanokvm_power_cycle` | `destructive` | composite: long press, wait, short press |
| `nanokvm_mount_iso` | `destructive` | `POST /api/storage/image/mount` |
| `nanokvm_unmount_iso` | `destructive` | `POST /api/storage/image/mount` (empty) |
| `nanokvm_hdmi_reset` | not destructive, idempotent | `POST /api/vm/hdmi/reset` — affects capture, not the target |
| `nanokvm_reset_hid` | not destructive, idempotent | `POST /api/hid/reset` — resets the USB gadget |

This folds the Python version's six separate input tools (`send_text`, `send_key`, `tap`,
`click`, `move`, `scroll`) into `nanokvm_input`.

## Transport and authentication

On-device means no stdio, so the endpoint is network-reachable and **must** authenticate
independently of tool annotations. This is transport security, not a guardrail.

- Streamable HTTP via the official `modelcontextprotocol/go-sdk` (v1.6.1)
- ~~Bearer token from `/etc/kvm/.nanokvm_mcp_token`, mode 0600, generated on first run~~
  **Corrected 2026-07-30 (#16):** as built, the token comes from the `NANOKVM_MCP_TOKEN`
  environment variable (`internal/config/config.go`), which the init script sources from
  `/root/nanokvm-mcp/nanokvm-mcp.env`; if unset, one is generated per start and logged to
  `/data/nanokvm-mcp/daemon.log`. No `/etc/kvm/.nanokvm_mcp_token` is ever read — the only
  `/etc/kvm` token this project touches is the firmware's `.picoclaw_internal_token`.
- Constant-time comparison; non-bearer requests get 401
- **Default bind `127.0.0.1:8080`.** Exposing it requires explicit configuration. A
  keystroke injector should not become LAN-reachable by default.
- ~~The README documents Tailscale as the recommended exposure path. The stock firmware
  already supports installing it, so putting this on a tailnet instead of the LAN is the
  largest available security win and costs nothing.~~ **Superseded — see below.**

  **Corrected 2026-07-30 (#16).** The security reasoning holds — the LAN is the wrong
  place for this listener — but "costs nothing" was wrong, and the recommendation has
  changed to an SSH port-forward to the loopback bind. `tailscaled` on this hardware
  settles around 40 MB resident ([sipeed/NanoKVM#366](https://github.com/sipeed/NanoKVM/issues/366))
  against the 43 MB available measured in Device recon above, and roughly 86% of that
  heap is wireguard-go per-interface buffer pools rather than collectable garbage
  ([tailscale/tailscale#16258](https://github.com/tailscale/tailscale/issues/16258)), so
  `GOMEMLIMIT` does not recover it. Off-LAN reach, when it is needed, belongs on a
  subnet router on another host — not on the device.

## Guardrails

- **Annotations** on all 14 tools, as above.
- **Read-only mode.** `NANOKVM_MCP_READONLY=true` causes the 7 mutating tools to not be
  *registered* at all, so they never appear in `tools/list`. Refusing at call time would
  still invite attempts.
- **Audit log.** Every mutating call logs timestamp, tool, backend, arguments, and
  outcome to stderr; the init script redirects to a file.

  **Typed text is redacted by default** — logged as length plus a truncated hash, not
  content — because passwords are routinely typed through a KVM. `NANOKVM_MCP_AUDIT_FULL=true`
  opts into full text.

These matter more here than off-device: the daemon is long-lived and network-reachable,
so anything holding the bearer token can drive it, not only an interactive session.

## Staleness guard

The defect that motivated this design (`/api/vm/gpio/led`) must be impossible to ship
silently. `tools/apicheck`:

1. Fetches `server/router/*.go` from upstream at a pinned ref
2. Extracts route strings
3. Compares against a manifest of every path the sidecar calls
4. Fails if any path we depend on no longer exists

Runs as a normal `go test` and as a scheduled CI job, so upstream drift surfaces as a
red build rather than a 404 during an incident. Paths under `/api/picoclaw/` are checked
too but reported as a distinct severity, since they are explicitly internal and unstable.

## Testing

The Python fork's failure — 107 green tests over a fundamentally broken product — is the
specific outcome to avoid.

- **No mocking of the HTTP or HID transport.** Tests run against an `httptest` fake
  NanoKVM implementing real endpoint shapes and real status codes. This is a hard
  constraint, not a preference.
- Table-driven tests per backend, covering both `picoclawBackend` and `publicBackend`
  against the same fake, asserting identical observable behaviour.
- Golden tests for tool schemas and annotations, so an annotation regression fails CI.
- Backend-selection tests: token present/absent, endpoint 401/404, stale-and-reprobe.
- Audit redaction tests, including that full text does not leak at default settings.
- One opt-in smoke test against real hardware behind a `//go:build device` tag.

## Development environment

`mise` pins the toolchain, so the riscv64 cross-build is reproducible across machines and
CI:

```toml
# mise.toml
[tools]
go = "1.25.4"

[env]
CGO_ENABLED = "0"

[tasks.build]
run = "GOOS=linux GOARCH=riscv64 go build -trimpath -ldflags='-s -w' -o dist/nanokvm-mcp ./cmd/nanokvm-mcp"

[tasks.test]
run = "go test ./..."

[tasks.apicheck]
run = "go test ./tools/apicheck/..."

[tasks.sizecheck]
run = "test $(stat -f%z dist/nanokvm-mcp) -lt 15728640"

[tasks.deploy]
run = "scp dist/nanokvm-mcp root://…/ && ssh … /etc/init.d/S96nanokvm-mcp restart"
```

## Build and deployment

```
CGO_ENABLED=0 GOOS=linux GOARCH=riscv64 go build -trimpath -ldflags="-s -w"
```

No custom toolchain, no `patchelf`, no `dl_lib` dependency.

**Measured** for a minimal server using the SDK's streamable HTTP handler, cross-compiled
to `linux/riscv64`: 10.2 MB plain, **7.1 MB** with `-s -w` (`-trimpath` adds no size change,
only reproducibility). 216 packages link; `golang.org/x/tools` — the heaviest entry in the
SDK's `go.mod` — is fully dead-code-eliminated. Idle RSS measured at 8.1 MB via a native
build as a proxy.

Budget: **binary under 15 MB, resident under 25 MB**, against **43 MB available** measured
on-device (a 128 MB board, less than the ~98 MB the datasheet implied). The binary fits
easily; the ~18 MB resident headroom is real but thin, which is why the screenshot path
below is a hard constraint rather than a nicety.

### Memory strategy

The constraint is the screenshot path, not the runtime. With only ~18 MB of headroom over
the resident budget, a single 4K frame decoded to RGBA (~33 MB) would OOM the device. A
1920×1080 frame (~8 MB) is survivable; 4K is not. Go's stdlib `image/jpeg` has **no scaled
decode**: unlike libjpeg's `scale_denom`, it cannot decode at 1/2 or 1/4 scale, so full
decode is always paid before downscaling.

Consequences, which are binding design rules and not optimizations:

- **`picoclawBackend` never decodes.** Upstream already resized and JPEG-encoded on-device;
  its bytes pass straight through to the MCP response. No image memory enters our heap.
  This is a further reason to prefer it.
- **`publicBackend` must decode, and is expensive in both RAM and CPU** on a 1 GHz core. It
  caps the resolution it will serve and calls `debug.FreeOSMemory()` after. It is a
  correctness fallback, not a performance-equivalent one.
- **Bounded reads on the MJPEG stream** via `io.LimitReader`. The Python fork accumulates
  `buffer += chunk` with no cap (`client.py:461`); that is the anti-pattern.

`GOMEMLIMIT` is set in the init script rather than via `debug.SetMemoryLimit` in code, so
it stays tunable without a rebuild. It is the primary lever: default `GOGC=100` permits the
heap to double before collecting, which is wrong at this scale, and the limit also makes the
scavenger return pages to the OS more aggressively. `GOGC` is left alone — setting both is
counterproductive.

Deliberately rejected:

| Option | Why not |
|---|---|
| UPX | Shrinks the binary ~60% on disk but decompresses into RAM at startup, raising peak RSS. Trades plentiful storage for the scarce ~43 MB of free RAM. |
| `GODEBUG=madvdontneed=1` | Obsolete; the default since Go 1.16. |
| `GORISCV64` tuning | Verified default is already `rva20u64`, matching the C906. Its additional extensions are T-Head-custom rather than ratified, so Go will not emit them. |
| `sync.Pool` buffer pooling | Premature at 8 MB idle. |

Budgets are enforced, not aspirational: CI fails a riscv64 build over 15 MB, and the device
smoke test fails on resident memory over 25 MB. `net/http/pprof` is available behind a build
tag for hardware bring-up.

Install layout (confirmed against hardware — see Device recon):

- **`/root/nanokvm-mcp/`** — binary + config, on ext4 with real exec/permission bits.
  Updates move `/kvmapp` aside and `RemoveAll` only `/root/old` and `/root/.kvmcache`, so
  `/root/nanokvm-mcp/` is untouched by app updates.
- **`/data/nanokvm-mcp/`** — audit log, on the 21.5 GB exfat data partition. Keeps write
  wear off the small rootfs and survives a rootfs reflash (separate partition).
- **`/etc/init.d/S96nanokvm-mcp`** — init script; orders after `S95nanokvm`.

A full SD-card reflash still wipes `/root` and `/etc`; the install script is re-runnable and
that case is documented, not solved.

## Out of scope

- WebRTC and H.264 streaming — MJPEG capture only
- Terminal/SSH, network/WiFi, OLED, and script-execution tools
- Multi-device support — one sidecar runs per device by definition
- Any further work on the Python fork; it is abandoned, not maintained
- Off-device operation. If the sidecar proves impractical on hardware, that is a
  re-design, not a fallback mode to build now.

## Risks

| Risk | Mitigation |
|---|---|
| Install path not persistent across app updates | Resolved: `/root/nanokvm-mcp/` survives app updates (recon confirmed) |
| Upstream changes the internal picoclaw API | Behind an interface; `publicBackend` is a complete implementation, not a stub; apicheck flags it |
| Memory pressure on a thin ~18 MB headroom (43 MB available) | Measured 8.1 MB idle, 7.1 MB binary. `GOMEMLIMIT` in the init script; picoclaw path never decodes JPEG; `publicBackend` hard-caps resolution to keep decode under budget; enforced in CI and the device smoke test |
| `publicBackend` screenshot spikes memory (no scaled decode in Go's `image/jpeg`) | Resolution cap plus `debug.FreeOSMemory()`; documented as a degraded path, and the reason picoclaw is preferred |
| Session-lock contention with PicoClaw | Detected and surfaced as a clear tool error rather than a hang |
| Network-exposed keystroke injection | Loopback default bind, bearer auth, read-only mode, audit log, SSH port-forward guidance |
