# A session against real hardware

A walkthrough of a Claude Code session driving this MCP server against a real device.
Redacted (device key, addresses, hostname), lightly trimmed, and corrected where the
original session miscounted — otherwise it is what happened.

**Setup:** NanoKVM Beta, firmware 2.4.3. Target machine is a Proxmox VE host. The daemon
runs on the device and is reached over an SSH tunnel at `127.0.0.1:8080`; the client is
Claude Code. The daemon was started in **read-only** mode, which requires setting
`NANOKVM_MCP_READONLY=true` explicitly — it is *not* the default. Unset or `false` (the
shipped default, `internal/config/config.go:76`) registers all 14 tools.

The point of this page is the second step: read-only mode is *enforced by construction*,
not by convention. The mutating tools are never registered, so they never appear in
`tools/list` and a client cannot call what it cannot see.

---

## 1. Which tools exist

> **list the nanokvm tools you have**

At this point the daemon wasn't running yet, so no tools were loaded:

```
nanokvm: http://127.0.0.1:8080/ (HTTP) - ✘ Failed to connect — ConnectionRefused
```

What the repo registers (`internal/mcpserver/tools.go`) is the full set of **14 tools** —
7 read-only and 7 mutating:

| Read-only | Purpose |
|---|---|
| `nanokvm_screenshot` | Capture target screen as JPEG (`width`, `height`, `quality`) |
| `nanokvm_led_status` | Power + HDD LED state |
| `nanokvm_hdmi_status` | HDMI signal state and resolution |
| `nanokvm_list_images` | List ISOs on the device |
| `nanokvm_mounted_image` | Currently mounted ISO, if any |
| `nanokvm_info` | Device information |
| `nanokvm_hardware` | Hardware details |

| Mutating | Purpose | Annotation |
|---|---|---|
| `nanokvm_input` | Batch HID actions: click, move, type, hotkey, scroll, drag, wait (mouse coords normalized to `[0,1]`) | destructive |
| `nanokvm_power` | `power` (short), `power_long` (force off), `reset` | destructive |
| `nanokvm_power_cycle` | Force off, wait `off_ms` (default 3000), power on | destructive |
| `nanokvm_mount_iso` | Mount ISO as CD-ROM (default) or USB disk | destructive |
| `nanokvm_unmount_iso` | Unmount current ISO | destructive |
| `nanokvm_hdmi_reset` | Reset HDMI capture pipeline (capture only, not the target) | idempotent |
| `nanokvm_reset_hid` | Reset USB HID gadget when input stops working | idempotent |

## 2. What the daemon actually serves — 7, not 14

The daemon was started, and `tools/list` was re-read.

> **please look again, I think I started the daemon locally**

Connected — and **only 7 of the 14 tools are exposed**:

| Tool | Args |
|---|---|
| `nanokvm_screenshot` | `width`, `height`, `quality` (all optional, `0` = backend default) |
| `nanokvm_led_status` | — |
| `nanokvm_hdmi_status` | — |
| `nanokvm_list_images` | — |
| `nanokvm_mounted_image` | — |
| `nanokvm_info` | — |
| `nanokvm_hardware` | — |

All read-only. The seven mutating tools (`nanokvm_input`, `nanokvm_power`,
`nanokvm_power_cycle`, `nanokvm_mount_iso`, `nanokvm_unmount_iso`, `nanokvm_hdmi_reset`,
`nanokvm_reset_hid`) aren't registered, because the daemon is in read-only mode:

```go
// internal/mcpserver/server.go
func New(d Deps) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "nanokvm", Version: "0.1.0"}, nil)
	registerReadOnly(s, d)
	if !d.ReadOnly {          // ← server.go:24
		registerMutating(s, d)
	}
	return s
}
```

`d.ReadOnly` is fed from `NANOKVM_MCP_READONLY` (`internal/config/config.go:76`).

This is the part worth seeing directly. Read-only mode is not a tool annotation the client
is trusted to respect, and not a check inside a handler that could be bypassed by a
malformed call — the handlers **do not exist on the server**. A client in this mode has no
name to call, because `tools/list` never mentions one. The daemon's startup line prints
`readonly=<v>` (`cmd/nanokvm-mcp/main.go:96`) if you want to confirm which mode it came up
in.

## 3. Device info and LED state

> **okay what's the device info and power LED state?**

**Device info**
- App version: 2.4.3
- Image: `2024-08-17-18-13-713161.img`
- Device key: `<device-key>`
- Network: `<nanokvm-ip>` on eth0 (wired, IPv4), mDNS `<kvm-hostname>.local`

**LEDs**
- Power: off (`pwr: false`)
- HDD: `false`, but `hdd_available: false` too — that LED isn't wired on this board, so
  the value is meaningless rather than "idle"

So the target machine is currently powered off. Note this is the NanoKVM's reading of the
*target's* power LED, not the KVM's own state — the KVM is obviously up.

*(That conclusion is wrong, and the next step is what caught it.)*

## 4. A screenshot, and a contradiction

> **take a screenshot of the machine on the KVM**

The target is a Proxmox VE host sitting at a console login prompt:

```
Welcome to the Proxmox Virtual Environment. Please use your web browser to
configure this server - connect to:

  https://<target-ip>:8006/

pve login: ttttttttt
pve login: r
pve login: _
```

Two things worth noting:

1. **The machine is clearly on, which contradicts the `pwr: false` reading from a moment
   ago.** Treat the screenshot, not `nanokvm_led_status`, as ground truth for target power
   state. See [the power-LED caveat in the README](../README.md#the-power-led-can-read-false-while-the-target-is-running)
   for why this happens and how to check your own board.
2. There's stray input in the login buffer — `ttttttttt` and `r` were typed at the prompt,
   likely a stuck key or leftover HID noise from an earlier session. Nothing was submitted;
   it's sitting at a fresh prompt.

That buffer could not be cleared from this session: `nanokvm_input` isn't available in
read-only mode. Clearing it would mean restarting the daemon with mutating tools enabled
and sending an Escape/Ctrl-U to flush the line.

---

## What this session demonstrates

- **Read-only mode is structural.** 14 tools registered, 7 served. The gate is at
  registration (`internal/mcpserver/server.go:24`), not at call time, so the mutating tools
  are absent from `tools/list` rather than merely refused.
- **The tool surface is honest about its own limits.** `hdd_available: false` tells a caller
  that `hdd` is not a reading. The power LED has no such flag — which is exactly how the
  session reached a wrong conclusion in step 3 and had to correct it in step 4.
- **A screenshot is the reliable power-state check on this hardware.** Not the LED.

## Reproducing this

```sh
# With NANOKVM_MCP_READONLY=true in nanokvm-mcp.env, restarted: tools/list returns 7
npx @modelcontextprotocol/inspector --cli http://127.0.0.1:8080/ \
  --transport http --header "Authorization: Bearer ${NANOKVM_MCP_TOKEN}" \
  --method tools/list | grep -c '"name"'

# Unset or false (the shipped default), restarted: 14
```

See the [README](../README.md) for setup, configuration, and the security model.
