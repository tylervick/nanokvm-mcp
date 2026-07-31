//go:build device

package devicetest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxResidentKB is the resident-memory budget from the design spec. Device
// recon measured ~43 MB free on real hardware, so exceeding this leaves the
// firmware itself short rather than merely making the daemon fat.
const maxResidentKB = 25 * 1024

// pidFile matches PIDFILE in deploy/S96nanokvm-mcp.
const pidFile = "/var/run/nanokvm-mcp.pid"

// commName is what /proc/<pid>/comm reads for the daemon. The init script
// starts it as `sh -c "exec <daemon>"`, and the exec replaces the shell's
// image while keeping the PID that start-stop-daemon recorded.
const commName = "nanokvm-mcp"

// readOnlyTools must be present whatever the device's NANOKVM_MCP_READONLY
// setting is, since read-only mode only withholds the mutating half.
var readOnlyTools = []string{
	"nanokvm_screenshot",
	"nanokvm_led_status",
	"nanokvm_hdmi_status",
	"nanokvm_list_images",
	"nanokvm_mounted_image",
	"nanokvm_info",
	"nanokvm_hardware",
}

// TestDeviceSmoke exercises the deployed daemon against a live NanoKVM and
// checks it is inside the resident-memory budget. It is read-only: every tool
// it calls is annotated readOnlyHint, and nothing it does touches the target
// machine's keyboard, power, or mounts.
func TestDeviceSmoke(t *testing.T) {
	endpoint := mustEnv(t, "NANOKVM_MCP_ENDPOINT", "MCP endpoint, e.g. http://127.0.0.1:8080/ over the SSH tunnel")
	token := mustEnv(t, "NANOKVM_MCP_TOKEN", "bearer token, e.g. from /root/nanokvm-mcp/nanokvm-mcp.env")
	sshSpec := mustEnv(t, "NANOKVM_DEVICE_SSH", `ssh argv prefix, e.g. "ssh -S /tmp/nkvm.sock root@nanokvm"`)

	dev := device{ssh: strings.Fields(sshSpec)}
	if len(dev.ssh) == 0 {
		t.Fatal("NANOKVM_DEVICE_SSH is empty after splitting on whitespace")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	sess := connect(ctx, t, endpoint, token)

	// Subtests run in declaration order, and resident_memory is last on
	// purpose: it has to observe a daemon that has already served a
	// screenshot, because JPEG handling is the allocation-heavy path and a
	// daemon that has served nothing proves nothing about the budget.
	// `go test -shuffle=on` reorders top-level tests only, so this holds.

	t.Run("tools_list", func(t *testing.T) {
		res, err := sess.ListTools(ctx, nil)
		if err != nil {
			t.Fatalf("tools/list: %v", err)
		}
		got := make(map[string]*mcp.Tool, len(res.Tools))
		for _, tool := range res.Tools {
			got[tool.Name] = tool
		}
		for _, name := range readOnlyTools {
			tool, ok := got[name]
			if !ok {
				t.Errorf("%s missing from tools/list", name)
				continue
			}
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Errorf("%s is not annotated readOnlyHint: a client cannot tell it is safe to call", name)
			}
		}
		// Whether the mutating tools are registered depends on the device's
		// NANOKVM_MCP_READONLY setting, so report the mode instead of
		// asserting one.
		mode := "read-write"
		if len(res.Tools) == len(readOnlyTools) {
			mode = "read-only"
		}
		t.Logf("daemon exposes %d tools (%s mode)", len(res.Tools), mode)
	})

	t.Run("hardware", func(t *testing.T) {
		hw := structured(t, call(ctx, t, sess, "nanokvm_hardware", nil))
		version, _ := hw["version"].(string)
		if version == "" {
			t.Fatalf("nanokvm_hardware returned no version: %v", hw)
		}
		t.Logf("hardware version: %s", version)
	})

	t.Run("screenshot", func(t *testing.T) {
		res := call(ctx, t, sess, "nanokvm_screenshot", nil)
		var shot *mcp.ImageContent
		for _, c := range res.Content {
			if img, ok := c.(*mcp.ImageContent); ok {
				shot = img
				break
			}
		}
		if shot == nil {
			t.Fatalf("nanokvm_screenshot returned no image content (%d parts)", len(res.Content))
		}
		if shot.MIMEType != "image/jpeg" {
			t.Errorf("screenshot MIME type = %q, want image/jpeg", shot.MIMEType)
		}
		// Decode in full rather than reading the header: a truncated JPEG still
		// has a valid header, and truncation is exactly what a bounded-buffer
		// bug on the device would produce.
		img, err := jpeg.Decode(bytes.NewReader(shot.Data))
		if err != nil {
			t.Fatalf("screenshot is not a decodable JPEG (%d bytes): %v", len(shot.Data), err)
		}
		b := img.Bounds()
		if b.Dx() == 0 || b.Dy() == 0 {
			t.Fatalf("screenshot has zero extent: %v", b)
		}
		t.Logf("screenshot: %dx%d, %d bytes", b.Dx(), b.Dy(), len(shot.Data))
	})

	t.Run("resident_memory", func(t *testing.T) {
		kb := dev.residentKB(ctx, t)
		t.Logf("daemon VmRSS: %d kB of a %d kB budget (%d kB headroom)", kb, maxResidentKB, maxResidentKB-kb)
		if kb >= maxResidentKB {
			t.Errorf("daemon VmRSS %d kB is over the %d kB budget; the device has only ~43 MB free", kb, maxResidentKB)
		}
	})
}

// device runs commands on the NanoKVM over the caller's own ssh invocation.
type device struct {
	ssh []string // argv prefix, e.g. {"ssh", "-S", "/tmp/nkvm.sock", "root@nanokvm"}
}

// run executes remote on the device and returns its trimmed stdout. The remote
// command is passed as a single trailing argument rather than interpolated
// into a local `sh -c`, so nothing here re-parses it locally. Splitting the
// prefix on whitespace does mean an ssh option containing a space (a control
// socket path, say) is unsupported — keep those paths space-free.
func (d device) run(ctx context.Context, t *testing.T, remote string) string {
	t.Helper()
	args := append(append([]string{}, d.ssh[1:]...), remote)
	cmd := exec.CommandContext(ctx, d.ssh[0], args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("device command %q failed: %v\nstderr: %s", remote, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out))
}

// residentKB returns the deployed daemon's VmRSS in kilobytes.
func (d device) residentKB(ctx context.Context, t *testing.T) int {
	t.Helper()
	pid := d.run(ctx, t, "cat "+pidFile)
	if _, err := strconv.Atoi(pid); err != nil {
		t.Fatalf("%s does not hold a pid: %q", pidFile, pid)
	}
	// Read comm alongside status: a stale pidfile pointing at some other
	// process would otherwise yield a plausible RSS number that has nothing to
	// do with this daemon, and the test would pass for the wrong reason.
	out := d.run(ctx, t, fmt.Sprintf("cat /proc/%s/comm; awk '/^VmRSS:/{print $2}' /proc/%s/status", pid, pid))
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Fatalf("unexpected /proc output for pid %s: %q", pid, out)
	}
	if comm := strings.TrimSpace(lines[0]); comm != commName {
		t.Fatalf("pid %s is %q, not %q: stale %s?", pid, comm, commName, pidFile)
	}
	kb, err := strconv.Atoi(strings.TrimSpace(lines[1]))
	if err != nil {
		t.Fatalf("VmRSS for pid %s: %v (got %q)", pid, err, lines[1])
	}
	return kb
}

// bearerAuth attaches the daemon's bearer token to every request, the way an
// MCP client's configured Authorization header does.
type bearerAuth struct {
	token string
	base  http.RoundTripper
}

func (b bearerAuth) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(req)
}

func connect(ctx context.Context, t *testing.T, endpoint, token string) *mcp.ClientSession {
	t.Helper()
	// No http.Client.Timeout: the streamable transport holds a standalone SSE
	// GET open for the life of the session, and a client-wide deadline would
	// tear it down mid-test. Per-call deadlines come from the context instead.
	hc := &http.Client{Transport: bearerAuth{token: token, base: http.DefaultTransport}}
	c := mcp.NewClient(&mcp.Implementation{Name: "nanokvm-mcp-devicetest", Version: "1"}, nil)
	sess, err := c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: hc}, nil)
	if err != nil {
		t.Fatalf("connect to %s: %v\nis the SSH tunnel up and the daemon running?", endpoint, err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func call(ctx context.Context, t *testing.T, sess *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := sess.CallTool(cctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s reported an error: %s", name, textOf(res))
	}
	return res
}

// structured returns a tool's structured result as a map. The SDK hands the
// client `any` holding whatever the JSON decoded to, so this is an accessor
// with a useful failure message, not a conversion.
func structured(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	if res.StructuredContent == nil {
		t.Fatalf("tool returned no structured content: %s", textOf(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("re-marshal structured content: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("structured content is not a JSON object: %v (%s)", err, raw)
	}
	return m
}

func textOf(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if txt, ok := c.(*mcp.TextContent); ok {
			b.WriteString(txt.Text)
		}
	}
	if b.Len() == 0 {
		return "(no text content)"
	}
	return b.String()
}

// mustEnv reads a required variable. Missing configuration fails rather than
// skips: `-tags device` is already the opt-in, and a suite that quietly skips
// itself is how you end up with a green run against no hardware at all.
func mustEnv(t *testing.T, key, hint string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Fatalf("%s is not set (%s)", key, hint)
	}
	return v
}

// Extending this suite: the hardware chores tracked in #15 (validate the
// mutating tools on real hardware) and #8 (confirm the install survives a
// firmware app update) have the same shape as TestDeviceSmoke — connect to the
// deployed daemon, drive it, inspect the device with device.run. Both belong
// here as sibling TestDevice* functions rather than subtests, so a run can
// select one with -run. #15 needs a further opt-in beyond `-tags device`,
// because unlike this test it types on the target machine's console.
