# nanokvm-mcp

An [MCP](https://modelcontextprotocol.io) server for the [Sipeed NanoKVM](https://github.com/sipeed/NanoKVM),
written in Go and running **on the device itself** as a standalone daemon alongside the
stock firmware. It exposes screen capture, HID input (keyboard/mouse), power control, and
ISO mounting to MCP clients (Claude Code, Claude Desktop, or any MCP-speaking tool) over
an authenticated HTTP endpoint — so the KVM stays controllable even when the machine that
normally drives it is offline.

For the full design rationale (why on-device, why Go, memory budget, threat model), see
[`docs/superpowers/specs/2026-07-22-nanokvm-mcp-sidecar-design.md`](docs/superpowers/specs/2026-07-22-nanokvm-mcp-sidecar-design.md).

## License and attribution

Licensed under the **GNU General Public License v3.0** — see [`LICENSE`](LICENSE).

This project is not affiliated with Sipeed. It talks to the stock NanoKVM firmware over
its existing HTTP/WebSocket API and, on devices where it's available, an internal
"PicoClaw" API used by Sipeed's own on-device agent. Some logic (the CryptoJS-compatible
password encryption scheme, HID action semantics) is ported or reverse-engineered from
the GPL-3.0 upstream project [`sipeed/NanoKVM`](https://github.com/sipeed/NanoKVM); files
containing such code carry a header noting the origin. GPL-3.0 was chosen specifically so
that porting from upstream requires no separate derivative-work analysis.

## Install layout

| Path | Contents |
|---|---|
| `/root/nanokvm-mcp/nanokvm-mcp` | the daemon binary |
| `/root/nanokvm-mcp/nanokvm-mcp.env` | shell-sourced config (env var assignments) |
| `/etc/init.d/S96nanokvm-mcp` | BusyBox init script (start/stop/restart) |
| `/data/nanokvm-mcp/audit.log` | audit log (default path; see below) |
| `/data/nanokvm-mcp/daemon.log` | daemon stdout/stderr, including the generated bearer token if one wasn't configured |

`/root` is the device's ext4 rootfs; `/data` is a separate exfat data partition, chosen so
the audit log survives a rootfs update/reflash and doesn't add write wear to the rootfs.
Firmware app updates only touch `/kvmapp`, `/root/old`, and `/root/.kvmcache` — they do not
touch `/root/nanokvm-mcp/`.

## Building

Requires [mise](https://mise.jdx.dev/) (pins Go 1.25.4) and no CGO toolchain — the daemon
is a static `CGO_ENABLED=0` build for `linux/riscv64`.

```sh
mise run build        # dist/nanokvm-mcp, for the device (linux/riscv64)
mise run build-host   # dist/nanokvm-mcp-host, for your own machine (e.g. to run tests locally)
mise run test         # go test ./...
mise run apicheck     # checks that our route assumptions still match live upstream
mise run sizecheck    # builds, fails if the binary exceeds 15 MB
```

## Installing on a device

From a machine with SSH/SCP access to the NanoKVM (password auth; the stock firmware has
no SSH keys configured):

```sh
HOST=root@<nanokvm> ./deploy/install.sh
```

This builds the riscv64 binary, copies it and the init script to the device, writes a
starter `/root/nanokvm-mcp/nanokvm-mcp.env` if one doesn't already exist, and starts the
daemon via the init script. Re-running it is safe — it will not overwrite an existing
config file.

To manage the service directly on the device:

```sh
/etc/init.d/S96nanokvm-mcp start
/etc/init.d/S96nanokvm-mcp stop
/etc/init.d/S96nanokvm-mcp restart
```

## Configuration

Configuration is read from the environment; the init script sources
`/root/nanokvm-mcp/nanokvm-mcp.env` (plain `KEY=value` lines) before starting the daemon.

| Variable | Default | Description |
|---|---|---|
| `NANOKVM_HOST` | *(required)* | Host[:port] of the NanoKVM's own HTTP API. On-device this is normally `127.0.0.1`. |
| `NANOKVM_USER` | `admin` | NanoKVM web UI username, used to authenticate against the firmware API. |
| `NANOKVM_PASS` | `admin` | NanoKVM web UI password. Change this from the firmware default. |
| `NANOKVM_HTTPS` | `false` | Use `https`/`wss` instead of `http`/`ws` when talking to `NANOKVM_HOST`. |
| `NANOKVM_VERIFY_SSL` | `true` | Verify the TLS certificate when `NANOKVM_HTTPS=true`. |
| `NANOKVM_MCP_BIND` | `127.0.0.1:8080` | Address:port the MCP HTTP endpoint listens on. Loopback by default — see [Security](#security-model). |
| `NANOKVM_MCP_TOKEN` | *(generated)* | Bearer token required on every MCP request. If unset, a random token is generated at each startup and printed to the log (`/data/nanokvm-mcp/daemon.log` when run via the init script). Set a fixed value in `nanokvm-mcp.env` to avoid it changing on every restart. |
| `NANOKVM_MCP_READONLY` | `false` | If `true`, the 7 mutating tools (input, power, mount, etc.) are not registered at all — they don't appear in `tools/list`, so a client can't attempt them even if it tries. |
| `NANOKVM_MCP_AUDIT` | `/data/nanokvm-mcp/audit.log` | Path to the JSON-lines audit log of mutating tool calls. |
| `NANOKVM_MCP_AUDIT_FULL` | `false` | If `true`, log typed text in full. By default, typed text is redacted (see [Security](#security-model)). |
| `GOMEMLIMIT` | `24MiB` (set by the init script) | Go runtime soft memory limit, important on a device with ~43 MB of free RAM. Not read by application code directly — it's a standard Go runtime variable. |

Boolean variables accept `true`/`1` for true; anything else (including unset) is false,
except where the default above is `true`.

## Security model

The daemon is network-reachable and can type on and reboot the target machine, so it
authenticates every request independently of anything the MCP client does:

- **Loopback by default.** `NANOKVM_MCP_BIND` defaults to `127.0.0.1:8080` — the endpoint
  is not reachable from the LAN unless you deliberately rebind it.
- **Bearer-token authentication.** Every HTTP request must carry
  `Authorization: Bearer <token>`; the comparison is constant-time, and non-matching or
  missing tokens get `401 Unauthorized`. This applies transport-wide — it is not a tool
  annotation or a client-side convention.
- **Read-only mode.** Setting `NANOKVM_MCP_READONLY=true` removes all 7 mutating tools
  from the server entirely (they never appear in `tools/list`), leaving only the 7
  read-only tools (screenshot, LED/HDMI status, image listing, device info).
- **Audit log with redaction by default.** Every mutating call is logged as a JSON line
  (timestamp, tool, backend, arguments, outcome). Because passwords and other secrets are
  routinely typed through a KVM, typed text is redacted by default — logged as a length
  and a truncated hash, not the content. Set `NANOKVM_MCP_AUDIT_FULL=true` to log full
  text instead (useful for debugging, but keep the log file access-restricted if you do).

**Do not expose `NANOKVM_MCP_BIND` directly to the LAN or the internet.** If you need to
reach the sidecar from another machine, put it on a [Tailscale](https://tailscale.com)
tailnet instead — the stock NanoKVM firmware already supports installing Tailscale, so
this costs nothing and is the recommended exposure path. Bind the sidecar to the
tailscale interface address (or leave it on loopback and use `tailscale serve`/a local
SSH tunnel) rather than to `0.0.0.0`.

## Connecting an MCP client

Point your MCP client at the daemon's HTTP endpoint with the bearer token in the
`Authorization` header. For example, a generic streamable-HTTP MCP client config:

```json
{
  "mcpServers": {
    "nanokvm": {
      "url": "http://<device>:8080/",
      "headers": {
        "Authorization": "Bearer <token from nanokvm-mcp.env or the daemon log>"
      }
    }
  }
}
```

Replace `<device>` with the NanoKVM's Tailscale address (or `127.0.0.1` plus an SSH
tunnel/port-forward — see [Security](#security-model) for why the LAN address is
discouraged) and `<token>` with the value of `NANOKVM_MCP_TOKEN`, or the token printed to
`/data/nanokvm-mcp/daemon.log` if you didn't set one.

## Tools

14 tools are registered by default (7 in read-only mode):

| Tool | Kind | Description |
|---|---|---|
| `nanokvm_screenshot` | read-only | Capture the target's screen as a JPEG image. |
| `nanokvm_led_status` | read-only | Read the power and HDD LEDs (`hdd_available:false` on hardware without an HDD LED). |
| `nanokvm_hdmi_status` | read-only | Get HDMI signal state and resolution. |
| `nanokvm_list_images` | read-only | List available ISO images on the device. |
| `nanokvm_mounted_image` | read-only | Get the currently mounted ISO, if any. |
| `nanokvm_info` | read-only | Get NanoKVM device information. |
| `nanokvm_hardware` | read-only | Get NanoKVM hardware details. |
| `nanokvm_input` | mutating | Send a batch of HID actions (click, move, type, hotkey, scroll, drag, wait). |
| `nanokvm_power` | mutating | Press the power or reset button. |
| `nanokvm_power_cycle` | mutating | Force off, wait, then power on. |
| `nanokvm_mount_iso` | mutating | Mount an ISO image as CD-ROM or USB disk. |
| `nanokvm_unmount_iso` | mutating | Unmount the currently mounted ISO. |
| `nanokvm_hdmi_reset` | mutating | Reset the HDMI capture pipeline (affects capture, not the target). |
| `nanokvm_reset_hid` | mutating | Reset the USB HID gadget if keyboard/mouse input stops working. |

All tools carry MCP annotations (`readOnlyHint`, `destructiveHint`, `idempotentHint`) so a
client can distinguish, for example, reading the power LED from force-shutting-down the
target.

## Development

```sh
mise run test      # unit tests, no mocked HTTP/WS transport (httptest fakes only)
mise run apicheck  # fails if our assumed upstream routes have drifted
mise run sizecheck  # fails if the binary exceeds 15 MB
```

See [`docs/superpowers/specs/2026-07-22-nanokvm-mcp-sidecar-design.md`](docs/superpowers/specs/2026-07-22-nanokvm-mcp-sidecar-design.md)
for the design background, threat model, and memory strategy in full.
