package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tylervick/nanokvm-mcp/internal/audit"
	"github.com/tylervick/nanokvm-mcp/internal/backend"
	"github.com/tylervick/nanokvm-mcp/internal/nanokvm"
)

// fakeFirmware is an httptest server speaking the NanoKVM response envelope,
// recording every authenticated request the handlers cause the client to make.
type fakeFirmware struct {
	*httptest.Server
	mu       sync.Mutex
	data     map[string]any // path -> data payload
	requests []fakeReq
}

type fakeReq struct {
	Method string
	Path   string
	Body   string
}

func newFakeFirmware() *fakeFirmware {
	f := &fakeFirmware{data: map[string]any{}}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "nano-kvm-token", Value: "tok"})
		writeEnvelope(w, 0, map[string]string{"token": "tok"})
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if ck, err := r.Cookie("nano-kvm-token"); err != nil || ck.Value != "tok" {
			w.WriteHeader(http.StatusUnauthorized)
			writeEnvelope(w, -2, nil)
			return
		}
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, fakeReq{Method: r.Method, Path: r.URL.Path, Body: string(body)})
		data, ok := f.data[r.URL.Path]
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeEnvelope(w, 0, data)
	})

	f.Server = httptest.NewServer(mux)
	return f
}

func writeEnvelope(w http.ResponseWriter, code int, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "msg": "ok", "data": data})
}

func (f *fakeFirmware) serve(path string, data any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[path] = data
}

func (f *fakeFirmware) recorded() []fakeReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeReq(nil), f.requests...)
}

// recBackend records the Screenshot options and Input actions it receives.
type recBackend struct {
	mu         sync.Mutex
	shotOpts   []backend.ScreenshotOpts
	inputCalls [][]backend.Action
}

func (b *recBackend) Name() string { return "rec" }

func (b *recBackend) Screenshot(_ context.Context, opts backend.ScreenshotOpts) (backend.Shot, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.shotOpts = append(b.shotOpts, opts)
	return backend.Shot{JPEG: []byte{0xFF, 0xD8, 0xFF, 0xD9}, Width: 640, Height: 480}, nil
}

func (b *recBackend) Input(_ context.Context, actions []backend.Action) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.inputCalls = append(b.inputCalls, actions)
	return nil
}

type fixture struct {
	fake    *fakeFirmware
	backend *recBackend
	auditB  *bytes.Buffer
	session *mcp.ClientSession
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	fake := newFakeFirmware()
	t.Cleanup(fake.Close)

	kvm := nanokvm.New(nanokvm.ClientConfig{
		BaseURL:  fake.URL,
		Username: "admin",
		Password: "admin",
	})
	be := &recBackend{}
	var buf bytes.Buffer
	s := New(Deps{KVM: kvm, Backend: be, Audit: audit.New(&buf, false), ReadOnly: false})

	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := s.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return &fixture{fake: fake, backend: be, auditB: &buf, session: cs}
}

func (fx *fixture) call(t *testing.T, tool string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := fx.session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", tool, err)
	}
	return res
}

func textContent(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		return ""
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] is %T, want TextContent", res.Content[0])
	}
	return tc.Text
}

func structuredJSON(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestToolHandlers(t *testing.T) {
	tests := []struct {
		name  string
		tool  string
		args  map[string]any
		serve map[string]any // firmware path -> data
		// Expectations. Requests are checked in order against the non-login
		// requests the firmware recorded; bodyHas are substring matches.
		wantReqs []fakeReq
		wantText string   // substring of the text content
		wantJSON []string // substrings of the marshaled structured content
		wantErr  string   // if non-empty, IsError with this substring
		audited  bool     // expect an audit line for this tool
	}{
		{
			name:  "led_status reports alpha hdd led as available",
			tool:  "nanokvm_led_status",
			serve: map[string]any{"/api/vm/gpio": map[string]any{"pwr": true, "hdd": false}, "/api/vm/hardware": map[string]any{"version": "Alpha"}},
			wantReqs: []fakeReq{
				{Method: "GET", Path: "/api/vm/gpio"},
				{Method: "GET", Path: "/api/vm/hardware"},
			},
			wantJSON: []string{`"pwr":true`, `"hdd":false`, `"hdd_available":true`},
		},
		{
			name:     "hdmi_status returns firmware payload",
			tool:     "nanokvm_hdmi_status",
			serve:    map[string]any{"/api/vm/hdmi": map[string]any{"enabled": true, "width": 1920}},
			wantReqs: []fakeReq{{Method: "GET", Path: "/api/vm/hdmi"}},
			wantJSON: []string{`"enabled":true`, `"width":1920`},
		},
		{
			name:     "list_images wraps firmware list",
			tool:     "nanokvm_list_images",
			serve:    map[string]any{"/api/storage/image": []string{"debian.iso", "ubuntu.iso"}},
			wantReqs: []fakeReq{{Method: "GET", Path: "/api/storage/image"}},
			wantJSON: []string{`"images":["debian.iso","ubuntu.iso"]`},
		},
		{
			name:     "mounted_image returns firmware payload",
			tool:     "nanokvm_mounted_image",
			serve:    map[string]any{"/api/storage/image/mounted": map[string]any{"file": "debian.iso"}},
			wantReqs: []fakeReq{{Method: "GET", Path: "/api/storage/image/mounted"}},
			wantJSON: []string{`"file":"debian.iso"`},
		},
		{
			name:     "info returns firmware payload",
			tool:     "nanokvm_info",
			serve:    map[string]any{"/api/vm/info": map[string]any{"ip": "192.0.2.10", "mdns": "nanokvm-test"}},
			wantReqs: []fakeReq{{Method: "GET", Path: "/api/vm/info"}},
			wantJSON: []string{`"ip":"192.0.2.10"`, `"mdns":"nanokvm-test"`},
		},
		{
			name:     "hardware returns raw firmware payload",
			tool:     "nanokvm_hardware",
			serve:    map[string]any{"/api/vm/hardware": map[string]any{"version": "Beta", "soc": "sg2002"}},
			wantReqs: []fakeReq{{Method: "GET", Path: "/api/vm/hardware"}},
			wantJSON: []string{`"version":"Beta"`, `"soc":"sg2002"`},
		},
		{
			name:     "power short press",
			tool:     "nanokvm_power",
			args:     map[string]any{"action": "power"},
			serve:    map[string]any{"/api/vm/gpio": map[string]any{}},
			wantReqs: []fakeReq{{Method: "POST", Path: "/api/vm/gpio", Body: `"duration":800,"type":"power"`}},
			wantText: "power action sent: power",
			audited:  true,
		},
		{
			name:     "power long press forces off",
			tool:     "nanokvm_power",
			args:     map[string]any{"action": "power_long"},
			serve:    map[string]any{"/api/vm/gpio": map[string]any{}},
			wantReqs: []fakeReq{{Method: "POST", Path: "/api/vm/gpio", Body: `"duration":5000,"type":"power"`}},
			wantText: "power action sent: power_long",
			audited:  true,
		},
		{
			name:     "power reset",
			tool:     "nanokvm_power",
			args:     map[string]any{"action": "reset"},
			serve:    map[string]any{"/api/vm/gpio": map[string]any{}},
			wantReqs: []fakeReq{{Method: "POST", Path: "/api/vm/gpio", Body: `"duration":800,"type":"reset"`}},
			wantText: "power action sent: reset",
			audited:  true,
		},
		{
			name:    "power rejects unknown action",
			tool:    "nanokvm_power",
			args:    map[string]any{"action": "explode"},
			wantErr: `invalid action "explode"`,
			audited: true,
		},
		{
			name:  "power_cycle presses long then short",
			tool:  "nanokvm_power_cycle",
			args:  map[string]any{"off_ms": 1},
			serve: map[string]any{"/api/vm/gpio": map[string]any{}},
			wantReqs: []fakeReq{
				{Method: "POST", Path: "/api/vm/gpio", Body: `"duration":5000`},
				{Method: "POST", Path: "/api/vm/gpio", Body: `"duration":800`},
			},
			wantText: "power cycled (waited 1ms)",
			audited:  true,
		},
		{
			name:     "mount_iso posts file and cdrom flag",
			tool:     "nanokvm_mount_iso",
			args:     map[string]any{"file": "/data/debian.iso", "cdrom": true},
			serve:    map[string]any{"/api/storage/image/mount": map[string]any{}},
			wantReqs: []fakeReq{{Method: "POST", Path: "/api/storage/image/mount", Body: `"cdrom":true,"file":"/data/debian.iso"`}},
			wantText: "mounted /data/debian.iso",
			audited:  true,
		},
		{
			name:     "unmount_iso posts empty mount",
			tool:     "nanokvm_unmount_iso",
			serve:    map[string]any{"/api/storage/image/mount": map[string]any{}},
			wantReqs: []fakeReq{{Method: "POST", Path: "/api/storage/image/mount", Body: `{}`}},
			wantText: "unmounted",
			audited:  true,
		},
		{
			name:     "hdmi_reset posts reset",
			tool:     "nanokvm_hdmi_reset",
			serve:    map[string]any{"/api/vm/hdmi/reset": map[string]any{}},
			wantReqs: []fakeReq{{Method: "POST", Path: "/api/vm/hdmi/reset"}},
			wantText: "hdmi reset",
			audited:  true,
		},
		{
			name:     "reset_hid posts hid reset",
			tool:     "nanokvm_reset_hid",
			serve:    map[string]any{"/api/hid/reset": map[string]any{}},
			wantReqs: []fakeReq{{Method: "POST", Path: "/api/hid/reset"}},
			wantText: "hid reset",
			audited:  true,
		},
		{
			name:    "led_status propagates firmware failure",
			tool:    "nanokvm_led_status",
			serve:   map[string]any{}, // /api/vm/gpio not served -> 404
			wantErr: "HTTP 404",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFixture(t)
			for path, data := range tc.serve {
				fx.fake.serve(path, data)
			}

			res := fx.call(t, tc.tool, tc.args)

			if tc.wantErr != "" {
				if !res.IsError {
					t.Fatalf("expected IsError, got success: %v", res.Content)
				}
				if txt := textContent(t, res); !strings.Contains(txt, tc.wantErr) {
					t.Errorf("error text %q does not contain %q", txt, tc.wantErr)
				}
			} else if res.IsError {
				t.Fatalf("unexpected tool error: %s", textContent(t, res))
			}

			if tc.wantText != "" {
				if txt := textContent(t, res); !strings.Contains(txt, tc.wantText) {
					t.Errorf("text %q does not contain %q", txt, tc.wantText)
				}
			}
			for _, want := range tc.wantJSON {
				if got := structuredJSON(t, res); !strings.Contains(got, want) {
					t.Errorf("structured content %s does not contain %s", got, want)
				}
			}

			if tc.wantReqs != nil {
				got := fx.fake.recorded()
				if len(got) != len(tc.wantReqs) {
					t.Fatalf("firmware saw %d requests %v, want %d", len(got), got, len(tc.wantReqs))
				}
				for i, want := range tc.wantReqs {
					if got[i].Method != want.Method || got[i].Path != want.Path {
						t.Errorf("request %d: got %s %s, want %s %s", i, got[i].Method, got[i].Path, want.Method, want.Path)
					}
					if want.Body != "" && !strings.Contains(got[i].Body, want.Body) {
						t.Errorf("request %d body %q does not contain %q", i, got[i].Body, want.Body)
					}
				}
			}

			auditLog := fx.auditB.String()
			if tc.audited {
				if !strings.Contains(auditLog, `"tool":"`+tc.tool+`"`) {
					t.Errorf("audit log missing entry for %s: %q", tc.tool, auditLog)
				}
				wantOK := `"ok":true`
				if tc.wantErr != "" {
					wantOK = `"ok":false`
				}
				if !strings.Contains(auditLog, wantOK) {
					t.Errorf("audit log missing %s: %q", wantOK, auditLog)
				}
			} else if auditLog != "" {
				t.Errorf("read-only tool %s wrote to the audit log: %q", tc.tool, auditLog)
			}
		})
	}
}

func TestScreenshotHandlerPassesOptsAndReturnsJPEG(t *testing.T) {
	fx := newFixture(t)

	res := fx.call(t, "nanokvm_screenshot", map[string]any{"width": 800, "height": 600, "quality": 70})

	if res.IsError {
		t.Fatalf("unexpected error: %s", textContent(t, res))
	}
	img, ok := res.Content[0].(*mcp.ImageContent)
	if !ok {
		t.Fatalf("content[0] is %T, want ImageContent", res.Content[0])
	}
	if img.MIMEType != "image/jpeg" {
		t.Errorf("MIMEType = %q, want image/jpeg", img.MIMEType)
	}
	if !bytes.Equal(img.Data, []byte{0xFF, 0xD8, 0xFF, 0xD9}) {
		t.Errorf("Data = %x, want backend JPEG bytes", img.Data)
	}
	want := backend.ScreenshotOpts{Width: 800, Height: 600, Quality: 70}
	if len(fx.backend.shotOpts) != 1 || fx.backend.shotOpts[0] != want {
		t.Errorf("backend saw opts %+v, want [%+v]", fx.backend.shotOpts, want)
	}
	if fx.auditB.String() != "" {
		t.Errorf("screenshot wrote to the audit log: %q", fx.auditB.String())
	}
}

func TestInputHandlerForwardsActionsToBackend(t *testing.T) {
	fx := newFixture(t)

	res := fx.call(t, "nanokvm_input", map[string]any{
		"actions": []map[string]any{
			{"action": "click", "x": 0.5, "y": 0.25, "button": "left"},
			{"action": "type", "text": "hello"},
		},
	})

	if res.IsError {
		t.Fatalf("unexpected error: %s", textContent(t, res))
	}
	if txt := textContent(t, res); !strings.Contains(txt, "executed 2 actions") {
		t.Errorf("text = %q, want executed 2 actions", txt)
	}
	if len(fx.backend.inputCalls) != 1 {
		t.Fatalf("backend Input called %d times, want 1", len(fx.backend.inputCalls))
	}
	got := fx.backend.inputCalls[0]
	if len(got) != 2 || got[0].Action != "click" || got[0].Button != "left" || got[1].Text != "hello" {
		t.Errorf("backend saw actions %+v", got)
	}
	if got[0].X == nil || *got[0].X != 0.5 || got[0].Y == nil || *got[0].Y != 0.25 {
		t.Errorf("click coords not forwarded: %+v", got[0])
	}
	if !strings.Contains(fx.auditB.String(), `"tool":"nanokvm_input"`) {
		t.Errorf("audit log missing nanokvm_input entry: %q", fx.auditB.String())
	}
}
