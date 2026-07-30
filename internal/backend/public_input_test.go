package backend

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/tylervick/nanokvm-mcp/internal/nanokvm"
)

const fakeToken = "session-abc"

// frame is one /api/ws message the firmware accepted: the event byte, and the
// HID report that followed it.
type frame struct {
	event  byte
	report []byte
}

// wsRecorder applies the dispatch upstream's server applies in
// server/service/ws/client.go Read(): messages are binary, byte 0 selects the
// event, and the rest must be a well-formed HID report — 8 bytes for the
// keyboard, 4 (relative) or 6 (absolute) for the mouse. Upstream silently drops
// everything else, so this records rejects instead of failing the read.
//
// Recording the rejects is the point. A fake that json.Unmarshal'd our own
// output would confirm we encode what we encode, and would stay green against a
// backend whose every message the firmware ignores.
type wsRecorder struct {
	mu       sync.Mutex
	wg       sync.WaitGroup
	accepted []frame
	rejected []string
}

func (r *wsRecorder) record(data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()

	reject := func(format string, args ...any) {
		r.rejected = append(r.rejected, fmt.Sprintf(format, args...))
	}
	if len(data) == 0 {
		reject("empty message")
		return
	}
	report := data[1:]
	switch data[0] {
	case 0: // heartbeat carries no report
		if len(report) != 0 {
			reject("heartbeat with a %d-byte payload", len(report))
			return
		}
	case 1:
		if len(report) != 8 {
			reject("keyboard report is %d bytes, firmware requires 8 (%q)", len(report), data)
			return
		}
	case 2:
		if len(report) != 4 && len(report) != 6 {
			reject("mouse report is %d bytes, firmware requires 4 or 6 (%q)", len(report), data)
			return
		}
	default:
		reject("event byte %d matches no case in the firmware's switch (%q)", data[0], data)
		return
	}
	r.accepted = append(r.accepted, frame{event: data[0], report: append([]byte(nil), report...)})
}

// frames waits for the server side of every connection to drain, then returns
// what the firmware would have acted on. Waiting matters: Input closes the
// socket and returns before the handler has necessarily read the last message.
func (r *wsRecorder) frames(t *testing.T) []frame {
	t.Helper()
	r.wg.Wait()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, why := range r.rejected {
		t.Errorf("firmware would discard this message: %s", why)
	}
	return append([]frame(nil), r.accepted...)
}

// fakeFirmware is an httptest NanoKVM speaking the real response envelope, the
// real status codes, and the real /api/ws binary protocol.
type fakeFirmware struct {
	srv *httptest.Server
	ws  *wsRecorder

	mu        sync.Mutex
	pastes    []string
	pasteCode int  // envelope code returned by /api/hid/paste
	denyWS    bool // refuse the websocket upgrade, as the auth middleware would
}

func newFakeFirmware(t *testing.T) *fakeFirmware {
	t.Helper()
	f := &fakeFirmware{ws: &wsRecorder{}}

	// /api/ws and /api/hid/paste both sit behind middleware.CheckToken()
	// upstream, so an unauthenticated request never reaches the handler.
	authed := func(w http.ResponseWriter, r *http.Request) bool {
		ck, err := r.Cookie("nano-kvm-token")
		if err != nil || ck.Value != fakeToken {
			w.WriteHeader(http.StatusUnauthorized)
			writeEnvelope(w, -2, "unauthorized", nil)
			return false
		}
		return true
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "nano-kvm-token", Value: fakeToken})
		writeEnvelope(w, 0, "ok", map[string]string{"token": fakeToken})
	})

	mux.HandleFunc("/api/hid/paste", func(w http.ResponseWriter, r *http.Request) {
		if !authed(w, r) {
			return
		}
		var body struct {
			Content string `json:"content"`
			Langue  string `json:"langue"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.pastes = append(f.pastes, body.Content)
		code := f.pasteCode
		f.mu.Unlock()
		if code != 0 {
			writeEnvelope(w, code, "paste failed", nil)
			return
		}
		writeEnvelope(w, 0, "ok", nil)
	})

	mux.HandleFunc("/api/ws", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		deny := f.denyWS
		f.mu.Unlock()
		if deny {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !authed(w, r) {
			return
		}
		// Add before the handshake completes, so a Dial that has returned
		// implies a pending Done that frames() will wait for.
		f.ws.wg.Add(1)
		defer f.ws.wg.Done()

		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		for {
			_, data, err := c.Read(r.Context())
			if err != nil {
				return
			}
			f.ws.record(data)
		}
	})

	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func writeEnvelope(w http.ResponseWriter, code int, msg string, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "msg": msg, "data": data})
}

func (f *fakeFirmware) client() *nanokvm.Client {
	return nanokvm.New(nanokvm.ClientConfig{
		BaseURL: f.srv.URL, WSURL: "ws" + f.srv.URL[len("http"):] + "/api/ws",
		Username: "a", Password: "b", HTTPClient: f.srv.Client(),
	})
}

func (f *fakeFirmware) public() *Public { return NewPublic(f.client()) }

func (f *fakeFirmware) setPasteCode(code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pasteCode = code
}

func (f *fakeFirmware) setDenyWS() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.denyWS = true
}

func (f *fakeFirmware) pasted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.pastes...)
}

// absMouse decodes the 6-byte absolute report: buttons, x and y as
// little-endian 16-bit, then the wheel.
func absMouse(t *testing.T, f frame) (buttons byte, x, y uint16, wheel int8) {
	t.Helper()
	if f.event != 2 || len(f.report) != 6 {
		t.Fatalf("want an absolute mouse report, got event %d with %d bytes", f.event, len(f.report))
	}
	return f.report[0],
		binary.LittleEndian.Uint16(f.report[1:3]),
		binary.LittleEndian.Uint16(f.report[3:5]),
		int8(f.report[5])
}

// relMouse decodes the 4-byte relative report: buttons, then signed deltas.
func relMouse(t *testing.T, f frame) (buttons byte, dx, dy, wheel int8) {
	t.Helper()
	if f.event != 2 || len(f.report) != 4 {
		t.Fatalf("want a relative mouse report, got event %d with %d bytes", f.event, len(f.report))
	}
	return f.report[0], int8(f.report[1]), int8(f.report[2]), int8(f.report[3])
}

// keyReport decodes the 8-byte keyboard report: a modifier bitmap, a reserved
// byte, and up to six held usage codes.
func keyReport(t *testing.T, f frame) (mod byte, keys []byte) {
	t.Helper()
	if f.event != 1 || len(f.report) != 8 {
		t.Fatalf("want a keyboard report, got event %d with %d bytes", f.event, len(f.report))
	}
	if f.report[1] != 0 {
		t.Errorf("keyboard report byte 1 is reserved, want 0, got %d", f.report[1])
	}
	return f.report[0], f.report[2:]
}

// firstKey returns the single held usage code, asserting the other five slots
// are clear.
func firstKey(t *testing.T, f frame) (mod byte, key byte) {
	t.Helper()
	mod, keys := keyReport(t, f)
	for i, k := range keys[1:] {
		if k != 0 {
			t.Errorf("key slot %d holds %#x, want it clear", i+1, k)
		}
	}
	return mod, keys[0]
}

func TestPublicInputClickIsAcceptedByFirmware(t *testing.T) {
	f := newFakeFirmware(t)
	x, y := 0.5, 0.5
	err := f.public().Input(context.Background(), []Action{{Action: "click", X: &x, Y: &y, Button: "left"}})
	if err != nil {
		t.Fatal(err)
	}

	frames := f.ws.frames(t)
	if len(frames) != 3 {
		t.Fatalf("click produced %d frames, want 3 (move, press, release)", len(frames))
	}
	// 0.5 maps to the middle of the 1..32767 axis. Every frame repeats the
	// position, so the press and release land where the move went.
	for i, fr := range frames {
		_, gx, gy, wheel := absMouse(t, fr)
		if gx != 16384 || gy != 16384 {
			t.Errorf("frame %d at (%d,%d), want (16384,16384)", i, gx, gy)
		}
		if wheel != 0 {
			t.Errorf("frame %d moved the wheel by %d", i, wheel)
		}
	}
	for i, want := range []byte{0x00, 0x01, 0x00} {
		if got, _, _, _ := absMouse(t, frames[i]); got != want {
			t.Errorf("frame %d buttons = %#02x, want %#02x", i, got, want)
		}
	}
}

func TestPublicInputClickButtonsAreHIDBits(t *testing.T) {
	// The report carries a bitmask, not the browser's button index. Sending the
	// index means left (index 0) reads as "no button held" and the click never
	// happens, while right (index 2) presses middle.
	for _, tc := range []struct {
		button string
		want   byte
	}{
		{"left", 0x01},
		{"right", 0x02},
		{"middle", 0x04},
		{"", 0x01}, // unset defaults to left
	} {
		t.Run("button="+tc.button, func(t *testing.T) {
			f := newFakeFirmware(t)
			x, y := 0.25, 0.75
			err := f.public().Input(context.Background(),
				[]Action{{Action: "click", X: &x, Y: &y, Button: tc.button}})
			if err != nil {
				t.Fatal(err)
			}
			frames := f.ws.frames(t)
			if len(frames) != 3 {
				t.Fatalf("got %d frames, want 3", len(frames))
			}
			if got, _, _, _ := absMouse(t, frames[1]); got != tc.want {
				t.Errorf("press buttons = %#02x, want %#02x", got, tc.want)
			}
		})
	}
}

func TestPublicInputClickWithoutCoordsKeepsTheCursor(t *testing.T) {
	// A click with no position must not invent one: the absolute report has no
	// way to say "here", so naming a coordinate would teleport the pointer to
	// the top-left before pressing. The relative report has no position field.
	f := newFakeFirmware(t)
	err := f.public().Input(context.Background(), []Action{{Action: "click", Button: "right"}})
	if err != nil {
		t.Fatal(err)
	}

	frames := f.ws.frames(t)
	if len(frames) != 2 {
		t.Fatalf("got %d frames, want 2 (press, release)", len(frames))
	}
	for i, want := range []byte{0x02, 0x00} {
		btn, dx, dy, wheel := relMouse(t, frames[i])
		if btn != want {
			t.Errorf("frame %d buttons = %#02x, want %#02x", i, btn, want)
		}
		if dx != 0 || dy != 0 || wheel != 0 {
			t.Errorf("frame %d moved the pointer by (%d,%d) wheel %d, want no movement", i, dx, dy, wheel)
		}
	}
}

func TestPublicInputMoveCarriesNoButtons(t *testing.T) {
	f := newFakeFirmware(t)
	x, y := 0.0, 1.0
	if err := f.public().Input(context.Background(), []Action{{Action: "move", X: &x, Y: &y}}); err != nil {
		t.Fatal(err)
	}

	frames := f.ws.frames(t)
	if len(frames) != 1 {
		t.Fatalf("got %d frames, want 1", len(frames))
	}
	btn, gx, gy, _ := absMouse(t, frames[0])
	if btn != 0 {
		t.Errorf("move pressed buttons %#02x", btn)
	}
	// The axis is 1..32767: a bare move must not read as a click at the origin.
	if gx != 1 || gy != 32767 {
		t.Errorf("move went to (%d,%d), want (1,32767)", gx, gy)
	}
}

func TestPublicInputTypePutsShiftInTheModifierByte(t *testing.T) {
	// 'a' and 'A' are the same usage code; the difference is bit 1 of byte 0.
	// The batch leads with a move so it takes the websocket path rather than the
	// paste fast path.
	f := newFakeFirmware(t)
	x, y := 0.5, 0.5
	err := f.public().Input(context.Background(), []Action{
		{Action: "move", X: &x, Y: &y},
		{Action: "type", Text: "aA"},
	})
	if err != nil {
		t.Fatal(err)
	}

	frames := f.ws.frames(t)
	if len(frames) != 5 {
		t.Fatalf("got %d frames, want 5 (move + two press/release pairs)", len(frames))
	}
	for _, tc := range []struct {
		frame int
		mod   byte
		key   byte
		what  string
	}{
		{1, 0x00, 0x04, "'a' press"},
		{2, 0x00, 0x00, "'a' release"},
		{3, 0x02, 0x04, "'A' press"},
		{4, 0x00, 0x00, "'A' release"},
	} {
		mod, key := firstKey(t, frames[tc.frame])
		if mod != tc.mod || key != tc.key {
			t.Errorf("%s: modifier %#02x key %#02x, want modifier %#02x key %#02x",
				tc.what, mod, key, tc.mod, tc.key)
		}
	}
}

func TestPublicInputTypeSkipsUnmappableRunes(t *testing.T) {
	// 'é' has no usage code in the table. Skipping it is correct; sending key 0
	// would be a phantom keystroke.
	f := newFakeFirmware(t)
	x, y := 0.5, 0.5
	err := f.public().Input(context.Background(), []Action{
		{Action: "move", X: &x, Y: &y},
		{Action: "type", Text: "éa"},
	})
	if err != nil {
		t.Fatal(err)
	}

	frames := f.ws.frames(t)
	if len(frames) != 3 {
		t.Fatalf("got %d frames, want 3 (move + one press/release pair)", len(frames))
	}
	if mod, key := firstKey(t, frames[1]); mod != 0 || key != 0x04 {
		t.Errorf("press = modifier %#02x key %#02x, want modifier 0x00 key 0x04", mod, key)
	}
}

func TestPublicInputHotkeyPacksModifiersIntoOneByte(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys []string
		mod  byte
		key  byte
	}{
		{"all four modifiers", []string{"ctrl", "shift", "alt", "meta", "a"}, 0x0F, 0x04},
		{"ctrl+alt+delete", []string{"ctrl", "alt", "delete"}, 0x05, 0x4C},
		{"cmd aliases meta", []string{"cmd", "q"}, 0x08, 0x14},
		{"win aliases meta", []string{"win", "e"}, 0x08, 0x08},
		{"super aliases meta", []string{"super", "l"}, 0x08, 0x0F},
		{"unknown key name is dropped", []string{"ctrl", "nope"}, 0x01, 0x00},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeFirmware(t)
			err := f.public().Input(context.Background(), []Action{{Action: "hotkey", Keys: tc.keys}})
			if err != nil {
				t.Fatal(err)
			}
			frames := f.ws.frames(t)
			if len(frames) != 2 {
				t.Fatalf("got %d frames, want 2 (press, release)", len(frames))
			}
			// One modifier bitmap in byte 0 — not one modifier per slot, which
			// would put the keycode somewhere the firmware never reads it.
			if mod, key := firstKey(t, frames[0]); mod != tc.mod || key != tc.key {
				t.Errorf("press = modifier %#02x key %#02x, want modifier %#02x key %#02x",
					mod, key, tc.mod, tc.key)
			}
			if mod, key := firstKey(t, frames[1]); mod != 0 || key != 0 {
				t.Errorf("release = modifier %#02x key %#02x, want everything clear", mod, key)
			}
		})
	}
}

func TestPublicInputDragHoldsTheButtonThroughTheMove(t *testing.T) {
	// The whole drag stays on the absolute report, because upstream releases the
	// relative mouse the moment an absolute report arrives (and vice versa). A
	// drag that pressed on one device and moved on the other would drop the
	// button at the start point and select nothing — the predecessor project's
	// exact failure.
	f := newFakeFirmware(t)
	err := f.public().Input(context.Background(), []Action{{
		Action: "drag",
		From:   &Point{X: f64(0), Y: f64(0)},
		To:     &Point{X: f64(1), Y: f64(1)},
	}})
	if err != nil {
		t.Fatal(err)
	}

	frames := f.ws.frames(t)
	if len(frames) != 4 {
		t.Fatalf("got %d frames, want 4 (move, press, drag, release)", len(frames))
	}
	for i, want := range []struct {
		buttons byte
		x, y    uint16
	}{
		{0x00, 1, 1},         // move to the start
		{0x01, 1, 1},         // press there
		{0x01, 32767, 32767}, // travel with the button still down
		{0x00, 32767, 32767}, // release at the end
	} {
		btn, gx, gy, _ := absMouse(t, frames[i])
		if btn != want.buttons || gx != want.x || gy != want.y {
			t.Errorf("frame %d = buttons %#02x at (%d,%d), want buttons %#02x at (%d,%d)",
				i, btn, gx, gy, want.buttons, want.x, want.y)
		}
	}
}

func TestPublicInputScrollUsesTheWheelDelta(t *testing.T) {
	for _, tc := range []struct {
		name   string
		amount int
		want   int8
	}{
		{"up", 3, 3},
		{"down", -3, -3},
		{"clamped up", 5000, 127},
		{"clamped down", -5000, -127},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeFirmware(t)
			err := f.public().Input(context.Background(), []Action{{Action: "scroll", Amount: tc.amount}})
			if err != nil {
				t.Fatal(err)
			}
			frames := f.ws.frames(t)
			if len(frames) != 1 {
				t.Fatalf("got %d frames, want 1", len(frames))
			}
			// A relative report so the wheel turns without moving the pointer.
			btn, dx, dy, wheel := relMouse(t, frames[0])
			if wheel != tc.want {
				t.Errorf("wheel = %d, want %d", wheel, tc.want)
			}
			if btn != 0 || dx != 0 || dy != 0 {
				t.Errorf("scroll also sent buttons %#02x and movement (%d,%d)", btn, dx, dy)
			}
		})
	}
}

func TestPublicInputTextOnlyUsesThePasteAPI(t *testing.T) {
	// type and wait alone go over REST; the websocket is never dialled. Losing
	// this fast path would push whole documents through one keystroke at a time.
	f := newFakeFirmware(t)
	err := f.public().Input(context.Background(), []Action{
		{Action: "type", Text: "hello"},
		{Action: "wait", DurationMs: 1},
		{Action: "type", Text: "world"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := f.pasted(); len(got) != 2 || got[0] != "hello" || got[1] != "world" {
		t.Errorf("pasted %q, want [hello world]", got)
	}
	if frames := f.ws.frames(t); len(frames) != 0 {
		t.Errorf("text-only batch opened the websocket and sent %d frames", len(frames))
	}
}

func TestPublicInputPasteFailureIsReported(t *testing.T) {
	// A non-zero envelope code is a failure even though the HTTP status is 200.
	// Swallowing it would report keystrokes as delivered that never landed.
	f := newFakeFirmware(t)
	f.setPasteCode(-1)
	err := f.public().Input(context.Background(), []Action{{Action: "type", Text: "hello"}})
	if err == nil {
		t.Fatal("Input reported success after the firmware rejected the paste")
	}
}

func TestPublicInputWebsocketRefusalIsReported(t *testing.T) {
	// Same contract on the websocket path: a refused upgrade must not read as
	// "actions executed".
	f := newFakeFirmware(t)
	f.setDenyWS()
	x, y := 0.5, 0.5
	err := f.public().Input(context.Background(), []Action{{Action: "click", X: &x, Y: &y}})
	if err == nil {
		t.Fatal("Input reported success after the firmware refused the websocket")
	}
}

func TestPublicInputWaitHonorsContext(t *testing.T) {
	// No HTTP traffic expected: a wait-only batch never dials anything.
	p := NewPublic(nanokvm.New(nanokvm.ClientConfig{BaseURL: "http://127.0.0.1:0"}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	err := p.Input(ctx, []Action{{Action: "wait", DurationMs: MaxWaitMs}})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Input with canceled ctx = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("wait ignored cancellation, blocked %v", elapsed)
	}
}

func TestPublicInputWaitHonorsContextOnTheWebsocketPath(t *testing.T) {
	// The websocket loop has its own wait; cancelling must abandon the batch
	// rather than hold the session lock for the full duration.
	f := newFakeFirmware(t)
	ctx, cancel := context.WithCancel(context.Background())
	x, y := 0.5, 0.5
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := f.public().Input(ctx, []Action{
		{Action: "move", X: &x, Y: &y},
		{Action: "wait", DurationMs: MaxWaitMs},
		{Action: "click", X: &x, Y: &y},
	})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Input with canceled ctx = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("wait ignored cancellation, blocked %v", elapsed)
	}
}

func TestPublicInputRejectsMalformedCoords(t *testing.T) {
	f := newFakeFirmware(t)
	p := f.public()
	if err := p.Input(context.Background(), []Action{{Action: "move"}}); err == nil {
		t.Error("move without coords should return an error, not panic")
	}
	if err := p.Input(context.Background(), []Action{{Action: "drag", From: &Point{}, To: &Point{}}}); err == nil {
		t.Error("drag with empty points should return an error, not panic")
	}
	if frames := f.ws.frames(t); len(frames) != 0 {
		t.Errorf("rejected actions still put %d frames on the wire", len(frames))
	}
}

func TestNormToKVM(t *testing.T) {
	// The absolute report's axis is 0..32767, and 0 is a legal coordinate, so
	// the mapping starts at 1 to keep a normalized 0 distinguishable from an
	// unset field. ValidateActions keeps callers inside [0,1]; the clamps are
	// the backstop for anything that reaches here unvalidated.
	for _, tc := range []struct {
		in   float64
		want int
	}{
		{0, 1},
		{0.5, 16384},
		{1, 32767},
		{-0.5, 1},     // clamped low
		{-1000, 1},    // clamped low
		{1.5, 32767},  // clamped high
		{1000, 32767}, // clamped high
	} {
		if got := normToKVM(tc.in); got != tc.want {
			t.Errorf("normToKVM(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestPublicName(t *testing.T) {
	// Select() and the audit log both record this string; picoclaw returns
	// "picoclaw", and the two must stay distinguishable.
	if got := NewPublic(nil).Name(); got != "public" {
		t.Errorf("Name() = %q, want %q", got, "public")
	}
}
