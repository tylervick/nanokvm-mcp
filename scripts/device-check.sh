#!/bin/sh
# NanoKVM device recon for the MCP sidecar.
#
# Run ON the NanoKVM, as root:
#   scp scripts/device-check.sh root@<nanokvm>:/tmp/
#   ssh root@<nanokvm> sh /tmp/device-check.sh
#
# Read-only. Sends no HID input, changes no state, writes only under /tmp.
# Targets busybox ash - no bashisms.

PASS=0
FAIL=0
WARN=0

say()  { printf '\n=== %s ===\n' "$1"; }
ok()   { printf '  [ok]   %s\n' "$1"; PASS=$((PASS+1)); }
bad()  { printf '  [FAIL] %s\n' "$1"; FAIL=$((FAIL+1)); }
warn() { printf '  [warn] %s\n' "$1"; WARN=$((WARN+1)); }
info() { printf '         %s\n' "$1"; }

# Prefer curl, fall back to busybox wget.
if command -v curl >/dev/null 2>&1; then
    HTTP=curl
elif command -v wget >/dev/null 2>&1; then
    HTTP=wget
else
    HTTP=none
fi

# status_of URL [HEADER...] -> prints HTTP status code
status_of() {
    _url=$1; shift
    if [ "$HTTP" = curl ]; then
        _args=""
        for _h in "$@"; do _args="$_args -H \"$_h\""; done
        eval curl -s -o /dev/null -w '%{http_code}' $_args "'$_url'" 2>/dev/null
    else
        echo "n/a"
    fi
}

# ---------------------------------------------------------------------------
say "1. Platform"
uname -a
info "libc: $(ls /lib/ld-musl* 2>/dev/null || echo 'no musl ld found')"
case "$(uname -m)" in
    riscv64) ok "riscv64 confirmed - GOARCH=riscv64 is correct" ;;
    *)       bad "arch is $(uname -m), not riscv64 - revisit build target" ;;
esac
[ "$HTTP" = none ] && warn "neither curl nor wget present; HTTP checks will be skipped"
[ "$HTTP" = wget ] && warn "only busybox wget present; status-code checks unavailable"

# ---------------------------------------------------------------------------
say "2. Memory  (spec assumes ~98MB free)"
free -m 2>/dev/null || cat /proc/meminfo | head -5
AVAIL=$(awk '/MemAvailable/ {print int($2/1024)}' /proc/meminfo 2>/dev/null)
if [ -n "$AVAIL" ]; then
    info "MemAvailable: ${AVAIL} MB"
    if [ "$AVAIL" -ge 40 ]; then
        ok "at least 40MB available - 25MB resident budget is workable"
    else
        bad "only ${AVAIL}MB available - the 25MB budget needs revisiting"
    fi
fi

# ---------------------------------------------------------------------------
say "3. Filesystem layout and persistence"
df -h 2>/dev/null
printf '\n'
mount | grep -vE '^(proc|sysfs|devpts|tmpfs|devtmpfs)' 2>/dev/null

for d in /kvmapp /root /usr/local/bin /etc/init.d /data; do
    if [ -d "$d" ]; then
        MNT=$(df -P "$d" 2>/dev/null | awk 'NR==2 {print $6}')
        RO=$(mount | awk -v m="$MNT" '$3==m && $0 ~ /[(,]ro[,)]/ {print "ro"}')
        if [ -n "$RO" ]; then
            warn "$d exists on $MNT but is READ-ONLY"
        elif touch "$d/.mcpwrite" 2>/dev/null; then
            rm -f "$d/.mcpwrite"
            ok "$d writable (on $MNT)"
        else
            warn "$d exists on $MNT but is not writable"
        fi
    else
        info "$d does not exist"
    fi
done

# ---------------------------------------------------------------------------
say "4. Update-survival analysis"
info "Upstream replaces AppDir=/kvmapp wholesale on update:"
info "  MoveFilesRecursively(/kvmapp -> /root/old), then new build -> /kvmapp"
info "It also RemoveAll's /root/old and /root/.kvmcache each update."
printf '\n'
info "=> Do NOT install under: /kvmapp, /root/old, /root/.kvmcache"
info "=> Candidate install paths, in preference order:"
for d in /usr/local/bin /data /root; do
    [ -d "$d" ] && info "     $d"
done
[ -d /kvmapp ] && ok "/kvmapp present - AppDir constant confirmed on this device"

# ---------------------------------------------------------------------------
say "5. Init system"
ls /etc/init.d/ 2>/dev/null
if [ -f /etc/init.d/S95nanokvm ]; then
    ok "S95nanokvm present - S96nanokvm-mcp slot is free and orders after it"
else
    warn "S95nanokvm not found - check the init script naming assumption"
fi
[ -e /etc/init.d/S96nanokvm-mcp ] && warn "S96nanokvm-mcp already exists"

# ---------------------------------------------------------------------------
say "6. PicoClaw internal token"
TOKFILE=/etc/kvm/.picoclaw_internal_token
if [ -f "$TOKFILE" ]; then
    ok "$TOKFILE exists (mode $(stat -c %a "$TOKFILE" 2>/dev/null))"
    TOKEN=$(cat "$TOKFILE" 2>/dev/null | tr -d '\r\n')
    info "token length: ${#TOKEN} chars (value not printed)"
else
    warn "$TOKFILE absent - generated lazily on first picoclaw use."
    info "The web UI may need to touch PicoClaw once, or we use publicBackend only."
    TOKEN=""
fi

# ---------------------------------------------------------------------------
say "7. Public API routes  (401 = exists, 404 = gone)"
# The router does not set HandleMethodNotAllowed, so gin returns 404 for the
# wrong HTTP method - indistinguishable from a missing route. Probe each path
# with the method it is actually registered under.
probe() {
    _method=$1; _path=$2; _note=$3
    if [ "$HTTP" != curl ]; then info "$_path skipped - needs curl"; return; fi
    if [ "$_method" = POST ]; then
        C=$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1$_path" 2>/dev/null)
    else
        C=$(status_of "http://127.0.0.1$_path")
    fi
    case "$C" in
        400|401|403) ok  "$_method $_path -> $C (route exists)" ;;
        404)         if [ -n "$_note" ]; then ok "$_method $_path -> 404 ($_note)";
                     else bad "$_method $_path -> 404 (route we depend on is GONE)"; fi ;;
        200)         ok  "$_method $_path -> 200 (exists)" ;;
        *)           warn "$_method $_path -> $C" ;;
    esac
}
probe GET  /api/vm/gpio
probe GET  /api/vm/gpio/led "confirms the Python fork's LED bug - use GET /api/vm/gpio"
probe GET  /api/vm/hdmi
probe GET  /api/vm/info
probe GET  /api/vm/hardware
probe GET  /api/storage/image
probe GET  /api/storage/image/mounted
probe POST /api/hid/paste
probe POST /api/hid/reset
probe GET  /api/stream/mjpeg

# ---------------------------------------------------------------------------
say "8. PicoClaw internal endpoints"
if [ "$HTTP" = curl ] && [ -n "$TOKEN" ]; then
    TH="X-NanoKVM-Internal-Token: $TOKEN"
    SH="X-PicoClaw-Session-ID: device-check"

    C=$(status_of "http://127.0.0.1/api/picoclaw/screenshot?format=base64&width=320" "$TH" "$SH")
    case "$C" in
        200) ok "GET /api/picoclaw/screenshot -> 200 - picoclawBackend is viable" ;;
        401) bad "screenshot -> 401 - token rejected; check loopback gate" ;;
        404) bad "screenshot -> 404 - endpoint absent; firmware too old?" ;;
        *)   warn "screenshot -> $C" ;;
    esac

    # wait/1ms is a genuine no-op: exercises auth + session lock, emits no HID.
    C=$(curl -s -o /tmp/.mcpact -w '%{http_code}' \
            -X POST "http://127.0.0.1/api/picoclaw/actions" \
            -H "$TH" -H "$SH" -H 'Content-Type: application/json' \
            -d '{"actions":[{"action":"wait","duration_ms":1}]}' 2>/dev/null)
    case "$C" in
        200) ok "POST /api/picoclaw/actions -> 200 - session lock accepts an arbitrary ID" ;;
        401) bad "actions -> 401 - token rejected" ;;
        404) bad "actions -> 404 - endpoint absent" ;;
        409|423) warn "actions -> $C - session lock held (PicoClaw running?)" ;;
        *)   warn "actions -> $C"; [ -s /tmp/.mcpact ] && info "body: $(head -c 200 /tmp/.mcpact)" ;;
    esac
    rm -f /tmp/.mcpact
else
    info "skipped - needs curl and a token"
fi

# ---------------------------------------------------------------------------
say "9. Firmware version"
cat /kvmapp/version 2>/dev/null || cat /etc/kvm/version 2>/dev/null || info "version file not found"
info "hw variant: $(cat /etc/kvm/hw 2>/dev/null || echo unknown)"

# ---------------------------------------------------------------------------
say "10. Static Go binary execution"
if [ -x /tmp/hello-riscv64 ]; then
    OUT=$(/tmp/hello-riscv64 2>&1)
    if [ "$OUT" = "hello from go" ]; then
        ok "CGO_ENABLED=0 riscv64 binary runs: $OUT"
    else
        bad "binary present but misbehaved: $OUT"
    fi
else
    info "no /tmp/hello-riscv64 - see step 2 of the instructions to test this"
fi

# ---------------------------------------------------------------------------
printf '\n=== SUMMARY ===\n'
printf '  pass %s / warn %s / fail %s\n' "$PASS" "$WARN" "$FAIL"
[ "$FAIL" -gt 0 ] && printf '  Failures above block the design as specified.\n'
exit 0
