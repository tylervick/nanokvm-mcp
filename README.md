# nanokvm-mcp

[![CI](https://github.com/tylervick/nanokvm-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/tylervick/nanokvm-mcp/actions/workflows/ci.yml) [![Release](https://img.shields.io/github/v/release/tylervick/nanokvm-mcp)](https://github.com/tylervick/nanokvm-mcp/releases) [![License](https://img.shields.io/github/license/tylervick/nanokvm-mcp)](LICENSE)

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
"PicoClaw" API used by Sipeed's own on-device agent. `internal/nanokvm/auth.go` is a
clean-room Go reimplementation of the CryptoJS-compatible password-encryption wire format
used by the GPL-3.0 upstream project [`sipeed/NanoKVM`](https://github.com/sipeed/NanoKVM)
and carries a header noting the origin. GPL-3.0 was chosen to match upstream and to keep
the freedom to port further logic from it in the future without a separate
derivative-work analysis.

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

Requires [mise](https://mise.jdx.dev/) (pins Go 1.26.5) and no CGO toolchain — the daemon
is a static `CGO_ENABLED=0` build for `linux/riscv64`.

```sh
mise run build        # dist/nanokvm-mcp, for the device (linux/riscv64)
mise run build-host   # dist/nanokvm-mcp-host, for your own machine (e.g. to run tests locally)
mise run test         # go test ./...
mise run apicheck     # checks that our route assumptions still match live upstream
mise run sizecheck    # builds, fails if the binary exceeds 15 MB
```

The build stamps the binary with `git describe --tags --always --dirty`, reported in the
startup log line (`nanokvm-mcp v0.1.0: backend=...`). Builds from an untagged checkout
show the commit hash instead.

### Releases

Releases are annotated git tags; pushing one triggers the release workflow,
which runs [GoReleaser](https://goreleaser.com) to build the riscv64 binary and
publish a GitHub release with the binary and checksums attached:

```sh
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0   # release workflow does the rest
```

Tags follow [semver](https://semver.org) (`vMAJOR.MINOR.PATCH`) and should be
cut from a clean checkout of `main`. To test the release build locally without
tagging: `mise x goreleaser -- goreleaser release --snapshot --clean`.

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

### First-run checklist (learned on real hardware)

Three things account for nearly every "it deployed but doesn't work" report:

1. **Set the real web-UI credentials.** The six REST-backed tools (LED, HDMI, info,
   hardware, ISO listing/mounting) authenticate against the firmware with
   `NANOKVM_USER`/`NANOKVM_PASS`. Your device is almost certainly *not* `admin`/`admin`
   anymore — the web UI forces a password change on first login. Put the real values in
   `/root/nanokvm-mcp/nanokvm-mcp.env`. The symptom of stale creds is asymmetric:
   `nanokvm_screenshot` and `nanokvm_input` keep working (they use the firmware's internal
   token, not your password) while every other tool fails to authenticate.
2. **Set an explicit `NANOKVM_MCP_TOKEN`.** If you leave it unset, a fresh token is
   generated at every restart and you have to fish it out of
   `/data/nanokvm-mcp/daemon.log` each time. Generate one once
   (`head -c 32 /dev/urandom | base64`) and put it in `nanokvm-mcp.env`.
3. **Check liveness via the pidfile.** The device's busybox has no `pgrep`; use
   `cat /var/run/nanokvm-mcp.pid` and `kill -0 $(cat /var/run/nanokvm-mcp.pid)`.

## Configuration

Configuration is read from the environment; the init script sources
`/root/nanokvm-mcp/nanokvm-mcp.env` (plain `KEY=value` lines) before starting the daemon.

| Variable | Default | Description |
|---|---|---|
| `NANOKVM_HOST` | *(required)* | Host[:port] of the NanoKVM's own HTTP API. On-device this is normally `127.0.0.1`. |
| `NANOKVM_USER` | `admin` | NanoKVM web UI username, used to authenticate against the firmware API. |
| `NANOKVM_PASS` | `admin` | NanoKVM web UI password. Must be the device's *actual* web password — see the [first-run checklist](#first-run-checklist-learned-on-real-hardware). |
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

**Do not expose `NANOKVM_MCP_BIND` directly to the LAN or the internet.** To reach the
sidecar from another machine, leave the bind on loopback and forward a local port over
SSH — see [Durable access](#durable-access-a-launchd-supervised-tunnel). That keeps two
independent layers in front of a keystroke injector: an SSH credential to reach the port
at all, and the bearer token to use it.

**A note on Tailscale, which this README previously recommended.** Putting the device on a
tailnet is sound reasoning about *exposure* — the LAN is the wrong place for this listener
— but running `tailscaled` **on the NanoKVM does not fit its memory budget**, and the
earlier claim here that it "costs nothing" was wrong. On this hardware `tailscaled` settles
around **40 MB resident** ([sipeed/NanoKVM#366](https://github.com/sipeed/NanoKVM/issues/366)),
against the **43 MB available** this project measures on-device. Upstream reports it
needing `GOMEMLIMIT=100`, `GOGC=50`, and a cron reboot every two days to stay up — and
still being OOM-killed at 78 MB — on a board with *more* free RAM than ours
([sipeed/NanoKVM#660](https://github.com/sipeed/NanoKVM/issues/660)). Tuning does not
recover it: roughly 86% of that heap is wireguard-go per-interface buffer pools, an
allocation floor rather than collectable garbage
([tailscale/tailscale#16258](https://github.com/tailscale/tailscale/issues/16258)). An OOM
here is not graceful — the kernel takes the largest resident process, which may be the
firmware's own video pipeline, so the KVM function dies along with your way back in.

If you later need to reach the device from outside the LAN, run the Tailscale **subnet
router on another always-on host** on that LAN rather than on the NanoKVM. The device
spends no RAM, the tunnel below rides over the tailnet unchanged, and the MCP listener
still never leaves loopback.

## Connecting an MCP client

Point your MCP client at the daemon's HTTP endpoint with the bearer token in the
`Authorization` header. For example, a generic streamable-HTTP MCP client config:

```json
{
  "mcpServers": {
    "nanokvm": {
      "url": "http://127.0.0.1:8080/",
      "headers": {
        "Authorization": "Bearer ${NANOKVM_MCP_TOKEN}"
      }
    }
  }
}
```

The URL is `127.0.0.1` because the sidecar stays bound to loopback and you reach it
through an SSH port-forward — see [Security](#security-model) for why the LAN address is
discouraged, and [Durable access](#durable-access-a-launchd-supervised-tunnel) for a
forward that survives reboots. `NANOKVM_MCP_TOKEN` holds the device's token, or the one
printed to `/data/nanokvm-mcp/daemon.log` if you didn't set an explicit value.

### Claude Code over an SSH tunnel (tested end-to-end)

This is the setup validated against real hardware. It uses an SSH master
connection that carries the port-forward and authenticates once; every later
`ssh`/`scp` rides the same socket without re-prompting. (The master exits after
an hour *idle*, so this is a per-session flow, not a permanent one — for
day-to-day use, set up the supervised tunnel in
[Durable access](#durable-access-a-launchd-supervised-tunnel) instead.)

**1. Open the master connection + tunnel** (one password prompt):

```sh
ssh -f -N -M -S /tmp/nkvm.sock -o ControlPersist=3600 -o ServerAliveInterval=30 \
  -o ExitOnForwardFailure=yes \
  -o PubkeyAuthentication=no -o PreferredAuthentications=password -o IdentitiesOnly=yes \
  -L 8080:127.0.0.1:8080 root@<device>
```

`ExitOnForwardFailure=yes` makes a failed port-forward (e.g. 8080 already taken
by a stale tunnel) fail loudly here instead of backgrounding a broken master.

The `PubkeyAuthentication=no` trio matters: the stock firmware has no authorized
keys, and an ssh-agent holding several keys will exhaust dropbear's auth attempts
before password auth is ever offered ("Too many authentication failures").

Check whether the tunnel is still alive later (`ControlPersist=3600` means the
master exits after 3600 s with no client connections — active use keeps it open):

```sh
ssh -S /tmp/nkvm.sock -O check root@<device>   # "Master running" = alive
```

**2. Pull the bearer token to a local file** — by reference from here on, so the
secret never enters shell history or configs:

```sh
(umask 077; ssh -S /tmp/nkvm.sock root@<device> \
  'sed -n "s/^NANOKVM_MCP_TOKEN=//p" /root/nanokvm-mcp/nanokvm-mcp.env' \
  > ~/.nanokvm-token) && test -s ~/.nanokvm-token || echo "no token in env file"
```

The `test -s` guard catches the case where no explicit `NANOKVM_MCP_TOKEN` is
set on the device (the generated-token path prints to `daemon.log` instead —
but set an explicit one; see the [first-run checklist](#first-run-checklist-learned-on-real-hardware)).

**3. Register with Claude Code** — the single quotes are load-bearing: they pass
the `${VAR}` reference through unexpanded, and Claude Code's `.mcp.json` expands
it at connection time, so the saved config holds the reference, not the secret:

```sh
export NANOKVM_MCP_TOKEN=$(cat ~/.nanokvm-token)   # add to your shell profile
claude mcp add --transport http nanokvm http://127.0.0.1:8080/ \
  --header 'Authorization: Bearer ${NANOKVM_MCP_TOKEN}'
```

**4. Use it.** Start a *fresh* Claude Code session (new MCP servers are picked up
at session start) from a shell where the variable is set, and ask for a
screenshot or device info. In the default read-only mode the 7 read-only tools
appear; mutating tools require `NANOKVM_MCP_READONLY=false` in the device env
plus an init-script restart, after which they show up annotated as destructive —
Claude asks before using them, and every call lands in the audit log.

To sanity-check the endpoint without Claude, use MCP Inspector:

```sh
npx @modelcontextprotocol/inspector --cli http://127.0.0.1:8080/ \
  --transport http --header "Authorization: Bearer ${NANOKVM_MCP_TOKEN}" --method tools/list
```

Note the shell expands the token into the process's arguments here (Inspector
has no file-based header option), so it's briefly visible to `ps` — fine on a
single-user machine, but on a shared host prefer checking with curl's `@file`
header form instead.

### Durable access: a launchd-supervised tunnel

The master-socket flow above authenticates once and lapses after an hour idle —
right for a working session, wrong for day-to-day. For a forward that comes back
on its own after a network drop, a device reboot, or a laptop wake, hand a plain
tunnel to launchd.

This changes nothing in the [security model](#security-model): the sidecar stays
bound to `127.0.0.1`, and reaching it still costs an SSH credential *plus* the
bearer token. What changes is only who restarts the tunnel.

This is a LAN-scoped path — it needs the client to reach the device's SSH port.
See [Security](#security-model) for the off-LAN extension.

**1. Install a key on the device.** An unattended reconnect can't answer a
password prompt, and the stock firmware ships no authorized keys. Check what the
device's dropbear supports first — ed25519 needs dropbear 2020.79 or newer:

```sh
ssh -v root@<device> exit 2>&1 | grep 'remote software version'
```

Generate a dedicated key (use `-t rsa -b 3072` instead if that banner is older)
and append it, paying the password prompt one last time. The `restrict` prefix
confines the key to what the tunnel actually needs:

```sh
ssh-keygen -t ed25519 -f ~/.ssh/nanokvm -C nanokvm-mcp -N ''
{ printf 'restrict,no-pty,command="/bin/false" '; cat ~/.ssh/nanokvm.pub; } | \
ssh -o PubkeyAuthentication=no -o PreferredAuthentications=password root@<device> \
  'mkdir -p /root/.ssh && chmod 700 /root/.ssh && cat >> /root/.ssh/authorized_keys \
   && chmod 600 /root/.ssh/authorized_keys'
```

**Understand what this key is before you install it.** `-N ''` gives it no
passphrase, because launchd has no one to ask for one — so `~/.ssh/nanokvm` is a
plaintext credential at rest, and whoever reads that file gets whatever the key
grants. Left unrestricted, that is a root shell on the device, not merely the
port-forward. Three things reduce the blast radius, in descending order of how
much they buy you:

- **The `restrict,no-pty,command="/bin/false"` prefix above.** `restrict` denies
  everything and re-enables nothing; `command="/bin/false"` forces a dead command
  for any shell or exec request. `ssh -N` asks for neither, so the forward still
  works while interactive use of the key does not. Dropbear parses `restrict` only
  on newer builds — if yours ignores it the key still works, just unrestricted, and
  if it rejects the line outright password auth still gets you in. Neither case
  locks you out, but check which you got (below) rather than assuming.
- **Keep it out of your agent.** Don't `ssh-add` this key. The plist references it
  by path, so the agent never needs it, and staying out of the agent is also what
  keeps the dropbear auth-attempt flood from coming back.
- **A passphrase plus a keychain-backed agent** (`ssh-add --apple-use-keychain`) is
  strictly safer at rest, but it defeats the point here: the whole reason for this
  section is a tunnel that reconnects with nobody logged in. If your threat model
  wants the passphrase, keep the per-session master-socket flow above instead and
  accept the hourly re-auth.

Some dropbear builds read `/etc/dropbear/authorized_keys` instead. Verify before
going further — a launchd job that can't authenticate just respawns forever. Test
the forward rather than a shell, since the forced command denies the latter by
design:

```sh
ssh -i ~/.ssh/nanokvm -o IdentitiesOnly=yes -f -N -L 18080:127.0.0.1:8080 root@<device> \
  && nc -z 127.0.0.1 18080 && echo "key auth + forwarding OK"
```

If that prints nothing, retry with `-v` and look for `Authentication succeeded`.
Should a shell come back instead of the forced command failing, the restriction
options were ignored — the key works but is unrestricted, so treat `~/.ssh/nanokvm`
accordingly. Clean up the test forward when done (`pkill -f 18080:127.0.0.1:8080`).

With a key in place, `-i ~/.ssh/nanokvm -o IdentitiesOnly=yes` *replaces* the
`PubkeyAuthentication=no` trio above rather than adding to it: dropbear is offered
exactly one key, so there is no agent flood to work around.

The key survives firmware *app* updates (they touch only `/kvmapp`, `/root/old`,
and `/root/.kvmcache`) but not a rootfs reflash — redo this step after one.

**2. Connect once interactively, then clear any ad-hoc master.** The first
connection pins the device's host key in `known_hosts`; the launchd job runs with
`StrictHostKeyChecking` at its default and will refuse an unknown host rather than
trust it blindly. Then release port 8080, or the supervised tunnel will fail its
forward and respawn in a loop:

```sh
ssh -S /tmp/nkvm.sock -O exit root@<device> 2>/dev/null
```

**3. Write the agent** to `~/Library/LaunchAgents/com.nanokvm.tunnel.plist`.
`ProgramArguments` gets no shell expansion, so spell out your home directory and
the device address in full — `~` and `$HOME` will not work. Replace
`YOUR-USER` and `DEVICE-ADDRESS` before loading it; unlike the shell snippets
elsewhere in this README, the placeholders here can't use angle brackets, which
XML would read as tags:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.nanokvm.tunnel</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/bin/ssh</string>
    <string>-N</string>
    <string>-i</string>              <string>/Users/YOUR-USER/.ssh/nanokvm</string>
    <string>-o</string>              <string>IdentitiesOnly=yes</string>
    <string>-o</string>              <string>ControlPath=none</string>
    <string>-o</string>              <string>ControlMaster=no</string>
    <string>-o</string>              <string>ExitOnForwardFailure=yes</string>
    <string>-o</string>              <string>ConnectTimeout=10</string>
    <string>-o</string>              <string>ServerAliveInterval=30</string>
    <string>-o</string>              <string>ServerAliveCountMax=3</string>
    <string>-L</string>              <string>8080:127.0.0.1:8080</string>
    <string>root@DEVICE-ADDRESS</string>
  </array>
  <key>RunAtLoad</key>          <true/>
  <key>KeepAlive</key>          <true/>
  <key>ThrottleInterval</key>   <integer>10</integer>
  <key>StandardErrorPath</key>  <string>/tmp/nanokvm-tunnel.log</string>
</dict>
</plist>
```

Four details there are load-bearing, and each maps to a way this fails silently:

- **No `-f`.** launchd supervises a *foreground* process. A self-backgrounding
  `ssh` looks like an instant exit and gets respawned forever.
- **`ControlPath=none`, not just `ControlMaster=no`.** These are not the same
  switch, and getting it wrong is the subtle one. Per `ssh_config(5)`,
  `ControlMaster=no` is the *default* and is precisely the value that lets a
  session **join** an existing master — it governs whether this `ssh` becomes a
  master, not whether it reuses one. Only `ControlPath=none` disables sharing. If
  your `~/.ssh/config` sets a `ControlPath`, the job would otherwise hand its
  forward to that master and exit immediately, giving you a respawn loop and a
  tunnel that dies whenever the master does.
- **`ExitOnForwardFailure=yes` with `ServerAliveInterval`/`ServerAliveCountMax`.**
  Together these make `ssh` exit within ~90 s of the link going away instead of
  holding a dead forward open. launchd can only restart something that exits.
- **`ConnectTimeout=10`.** Without it a connect to a sleeping or absent device sits
  in the system TCP timeout for over a minute before launchd gets its exit code,
  which stretches every reconnect attempt for no benefit on a LAN.

**4. Load it and confirm:**

```sh
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.nanokvm.tunnel.plist
launchctl print gui/$(id -u)/com.nanokvm.tunnel | grep -E 'state|pid'
nc -z 127.0.0.1 8080 && echo "tunnel up"
```

`nc -z` confirms the forward; to check the MCP endpoint end-to-end, use the MCP
Inspector command above. To stop it, or to reload after editing the plist:

```sh
launchctl bootout gui/$(id -u)/com.nanokvm.tunnel
```

With the tunnel supervised, the `claude mcp add` registration from step 3 above
needs no changes — it already points at `127.0.0.1:8080`, and now that address
answers without a manual re-auth first.

### Claude Desktop

Claude Desktop cannot reach this daemon directly. Its config file registers
**stdio** servers only — there is no `url`/`headers` form — and the Connectors UI
offers OAuth alone for custom remote servers, with no bearer-token or custom-header
field ([anthropics/claude-ai-mcp#112](https://github.com/anthropics/claude-ai-mcp/issues/112),
closed as not planned). Bridge the HTTP endpoint to stdio with
[`mcp-remote`](https://github.com/geelen/mcp-remote).

| Platform | Config file |
|---|---|
| macOS | `~/Library/Application Support/Claude/claude_desktop_config.json` |
| Windows | `%APPDATA%\Claude\claude_desktop_config.json` |

Bring the [tunnel](#durable-access-a-launchd-supervised-tunnel) up first, then:

```json
{
  "mcpServers": {
    "nanokvm": {
      "command": "/bin/sh",
      "args": [
        "-c",
        "NANOKVM_MCP_TOKEN=$(cat ~/.nanokvm-token) exec /opt/homebrew/bin/npx -y mcp-remote http://127.0.0.1:8080/ --header 'Authorization: Bearer ${NANOKVM_MCP_TOKEN}'"
      ]
    }
  }
}
```

Quit and reopen Claude Desktop afterwards — closing the window does not reload
MCP servers.

Three details in that command matter:

- **The single quotes are load-bearing** — same reason as the Claude Code flow
  above, different consumer. They stop the shell expanding
  `${NANOKVM_MCP_TOKEN}`, so the literal reference reaches `mcp-remote`, which
  performs its own `${VAR}` substitution from its environment. The token is read
  out of `~/.nanokvm-token` (mode 0600) at launch and never appears in the config
  file. Claude Desktop's well-known refusal to expand `${VAR}` in `args` is
  therefore harmless here — passing it through untouched is exactly what we want.
  (`mcp-remote`'s own README suggests an `env` block instead, which writes the
  secret into the config JSON; this keeps the by-reference discipline.)
- **Use an absolute path to `npx`.** Claude Desktop launches servers from the GUI
  rather than a login shell, so your `PATH` is not its `PATH` — a bare `npx` is
  the most common reason this config silently fails to start. Find yours with
  `command -v npx`; `/opt/homebrew/bin/npx` above is the Homebrew location.
- **No `--allow-http`.** `mcp-remote` already exempts `localhost` and `127.0.0.1`
  from its HTTPS requirement, so the flag buys nothing here and would relax that
  check for every other host.

The token lands in the bridge process's *environment* rather than its arguments,
so it does not appear in a plain `ps` listing the way the Inspector command above
does.

### Other MCP clients

Cursor, VS Code, and Windsurf all speak streamable HTTP natively, so they need no
bridge — just the tunnel, plus each one's own syntax for pulling the token in by
reference instead of pasting it.

**Cursor** — `~/.cursor/mcp.json` (global) or `.cursor/mcp.json` (per project). A
remote server is one that has `url` instead of `command`; no `type` field is
needed. Interpolation is `${env:VAR}`:

```json
{
  "mcpServers": {
    "nanokvm": {
      "url": "http://127.0.0.1:8080/",
      "headers": { "Authorization": "Bearer ${env:NANOKVM_MCP_TOKEN}" }
    }
  }
}
```

**VS Code** — `.vscode/mcp.json` (workspace) or the user-profile `mcp.json`. The
top-level key is `servers`, not `mcpServers`, and HTTP servers need an explicit
`"type": "http"`. Prefer a `promptString` input over an environment variable: VS
Code asks once, keeps the value in its secret storage, and the token never lands
in the file at all.

```json
{
  "inputs": [
    {
      "type": "promptString",
      "id": "nanokvm-token",
      "description": "NanoKVM MCP bearer token",
      "password": true
    }
  ],
  "servers": {
    "nanokvm": {
      "type": "http",
      "url": "http://127.0.0.1:8080/",
      "headers": { "Authorization": "Bearer ${input:nanokvm-token}" }
    }
  }
}
```

**Windsurf** — `~/.codeium/windsurf/mcp_config.json`. The URL field is
`serverUrl`, not `url`; otherwise it matches Cursor:

```json
{
  "mcpServers": {
    "nanokvm": {
      "serverUrl": "http://127.0.0.1:8080/",
      "headers": { "Authorization": "Bearer ${env:NANOKVM_MCP_TOKEN}" }
    }
  }
}
```

Windsurf also supports `${file:/absolute/path}`, which reads a file's contents
straight into the header and is the closest any client gets to the by-reference
pattern. If you use it, strip the trailing newline from the token file first
(`printf %s "$(cat ~/.nanokvm-token)" > ~/.nanokvm-token.raw`) — an unstripped
newline in the header value will fail auth in a way that reads like a bad token.
Windsurf also needs a full quit and reopen after a config change.

**One caveat for the `${env:...}` configs above.** `export NANOKVM_MCP_TOKEN` in
your shell profile is picked up by Claude Code because it runs in your terminal.
A GUI-launched editor may not see it: on macOS an app started from Finder or the
Dock inherits launchd's environment, not your shell's. Either start the editor
from a terminal that has the variable set, or use a mechanism that doesn't depend
on it — VS Code's `${input:}` and Windsurf's `${file:}` both avoid the problem.
Reaching for `launchctl setenv` works but exports the token to every process you
own, which is a worse trade than either.

**Anything else.** Other clients fall into one of two shapes. If the client takes
a `url` plus a `headers` map, use the generic config at the top of this section
with whatever interpolation syntax it documents. If it only registers stdio
servers, wrap it in `mcp-remote` exactly as Claude Desktop does above. Either way
the tunnel and the bearer token are unchanged — only the file location and the
variable syntax differ.

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

For a walkthrough of a real session against hardware — including `tools/list` returning 14
tools normally and 7 in read-only mode — see [`docs/demo.md`](docs/demo.md).

### The power LED can read `false` while the target is running

On the board this was tested on (NanoKVM **Beta**, firmware 2.4.3), `nanokvm_led_status`
returned `pwr: false` while `nanokvm_screenshot` showed the target sitting at a Proxmox VE
console login prompt — plainly powered on.

**Treat a screenshot, not `nanokvm_led_status`, as ground truth for target power state.**

This is one observation on one board, not a general claim about all NanoKVM hardware.
**To check your own:** call `nanokvm_led_status` and `nanokvm_screenshot` back to back with
the target running. If `pwr` is `false` while the screen shows a live machine, the power-LED
sense path on your setup isn't reporting, and you should not automate against it. The stock
web UI's power icon reads the same GPIO, so it is an equivalent check.

Why the daemon cannot detect this for you: upstream reads the power LED from a single GPIO
(`/sys/class/gpio/gpio504/value`) and reports `pwr` as active-low — `value == 0` is `true` —
with no separate signal for "this line isn't connected"
([`server/service/vm/gpio.go`](https://github.com/sipeed/NanoKVM/blob/main/server/service/vm/gpio.go)).
A disconnected sense line and a powered-off target produce the same `false`.

Upstream does **not** document this as a board-version limitation, and its config points the
other way: the same `gpio504` power-LED path is configured for all three hardware versions —
Alpha, Beta, and PCIe
([`server/config/hardware.go`](https://github.com/sipeed/NanoKVM/blob/main/server/config/hardware.go)) —
so power-LED sensing is *expected* to work on Beta. Reading it requires the KVM-B / ATX board
wired to the host's 9-pin front-panel header (the Full version ships KVM-B; Lite does not),
and the PLED lead is polarized. A missing KVM-B, or an unconnected or reversed PLED lead,
would produce the reading observed here. Sipeed issue
[#241](https://github.com/sipeed/NanoKVM/issues/241) reports non-working PWR and HDD LEDs on a
Full version *with* the ATX daughterboard attached, and has no maintainer response — so the
root cause is not settled upstream. No schematic confirming the per-board sense wiring was
found.

The **HDD LED is a separate and documented case.** `hdd` is meaningful only on Alpha
hardware: upstream leaves `GPIOHDDLed` empty on Beta/PCIe and hardcodes `hdd = false` there,
and its user guide notes that on the Full version's ATX board "only the power, reset buttons,
and power light are exposed, so it is normal for the HDD LED to not light up." This daemon
surfaces that as `hdd_available: false` (`internal/nanokvm/vm.go`). **When `hdd_available` is
`false`, ignore `hdd` — it is not a reading.**

### Capture lags input by about one frame

On real hardware (firmware 2.4.3) the video capture pipeline trails HID input by roughly one
frame, so a `nanokvm_screenshot` taken immediately after `nanokvm_input` can come back showing
the screen *before* the input landed. This is firmware behavior, not a bug in this daemon.

If you are driving a screenshot → act → screenshot loop, let the frame catch up: end the
`nanokvm_input` batch with a `wait` action of ~100–200 ms (`{"action":"wait","duration_ms":150}`),
or take a second screenshot and use that one.

## Development

```sh
mise run test      # unit tests, no mocked HTTP/WS transport (httptest fakes only)
mise run apicheck  # fails if our assumed upstream routes have drifted
mise run sizecheck # fails if the binary exceeds 15 MB
```

See [`docs/superpowers/specs/2026-07-22-nanokvm-mcp-sidecar-design.md`](docs/superpowers/specs/2026-07-22-nanokvm-mcp-sidecar-design.md)
for the design background, threat model, and memory strategy in full.
