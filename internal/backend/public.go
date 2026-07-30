package backend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"time"

	"golang.org/x/image/draw"

	"github.com/coder/websocket"

	"github.com/tylervick/nanokvm-mcp/internal/hid"
	"github.com/tylervick/nanokvm-mcp/internal/nanokvm"
)

type Public struct{ kvm *nanokvm.Client }

func NewPublic(kvm *nanokvm.Client) *Public { return &Public{kvm: kvm} }
func (p *Public) Name() string              { return "public" }

const (
	maxDecodePixels = 2_100_000 // ~1920x1080; refuse to decode anything larger
	maxFrameBytes   = 8 << 20   // bound the stream read at 8MB
)

func (p *Public) Screenshot(ctx context.Context, opts ScreenshotOpts) (Shot, error) {
	tok, err := p.kvm.Token(ctx)
	if err != nil {
		return Shot{}, err
	}
	// Reconstruct the base URL via a request through the client's transport.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.kvm.BaseURL()+"/api/stream/mjpeg?n=1", nil)
	if err != nil {
		return Shot{}, err
	}
	req.AddCookie(&http.Cookie{Name: "nano-kvm-token", Value: tok})
	resp, err := p.kvm.HTTP().Do(req)
	if err != nil {
		return Shot{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		drainBody(resp.Body)
		return Shot{}, fmt.Errorf("public screenshot: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxFrameBytes))
	if err != nil {
		return Shot{}, err
	}
	jpegBytes, err := extractJPEG(raw)
	if err != nil {
		return Shot{}, err
	}

	// Inspect dimensions WITHOUT full decode.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(jpegBytes))
	if err != nil {
		return Shot{}, err
	}
	needResize := (opts.Width > 0 && cfg.Width > opts.Width) || (opts.Height > 0 && cfg.Height > opts.Height)
	if !needResize {
		// Clone so the returned JPEG doesn't keep the whole up-to-8MB read
		// buffer (raw) alive via a sub-slice reference.
		return Shot{JPEG: bytes.Clone(jpegBytes), Width: cfg.Width, Height: cfg.Height}, nil
	}
	if cfg.Width*cfg.Height > maxDecodePixels {
		return Shot{}, fmt.Errorf("frame %dx%d exceeds decode cap (%d px); use the picoclaw backend for on-device resize",
			cfg.Width, cfg.Height, maxDecodePixels)
	}
	return resizeJPEG(jpegBytes, opts)
}

func extractJPEG(buf []byte) ([]byte, error) {
	start := bytes.Index(buf, []byte{0xFF, 0xD8})
	if start < 0 {
		return nil, errors.New("no JPEG SOI marker in stream")
	}
	end := bytes.Index(buf[start:], []byte{0xFF, 0xD9})
	if end < 0 {
		return nil, errors.New("no JPEG EOI marker in stream")
	}
	return buf[start : start+end+2], nil
}

func resizeJPEG(in []byte, opts ScreenshotOpts) (Shot, error) {
	src, err := jpeg.Decode(bytes.NewReader(in))
	if err != nil {
		return Shot{}, err
	}
	b := src.Bounds()
	nw, nh := b.Dx(), b.Dy()
	if opts.Width > 0 && nw > opts.Width {
		nh = nh * opts.Width / nw
		nw = opts.Width
	}
	if opts.Height > 0 && nh > opts.Height {
		nw = nw * opts.Height / nh
		nh = opts.Height
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	// draw.Src: the JPEG source is opaque, so no alpha blending is needed;
	// Src skips the read-modify-write of Over on this CPU-bound path.
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	q := opts.Quality
	if q <= 0 {
		q = 80
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: q}); err != nil {
		return Shot{}, err
	}
	return Shot{JPEG: out.Bytes(), Width: nw, Height: nh}, nil
}

// sleepCtx sleeps for ms milliseconds or until ctx is done, whichever is first.
func sleepCtx(ctx context.Context, ms int) error {
	t := time.NewTimer(time.Duration(ms) * time.Millisecond)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// normToKVM maps a normalized [0,1] coordinate onto the absolute mouse axis.
// The axis runs 0..32767, but the mapping starts at 1 so a normalized 0 stays
// distinguishable from an unset field on the wire.
func normToKVM(v float64) int {
	k := int(v*0x7FFE) + 1
	if k < 1 {
		k = 1
	}
	if k > hid.MaxCoord {
		k = hid.MaxCoord
	}
	return k
}

// mouseButton maps the API's button names onto HID button bits. These are bits,
// not indices: an unset button defaults to left, which is bit 0 and not 0.
var mouseButton = map[string]byte{
	"left":   hid.ButtonLeft,
	"middle": hid.ButtonMiddle,
	"right":  hid.ButtonRight,
	"":       hid.ButtonLeft,
}

func (p *Public) Input(ctx context.Context, actions []Action) error {
	if err := ValidateActions(actions); err != nil {
		return err
	}
	// text-only fast path uses the REST paste API; no websocket needed.
	onlyText := true
	for _, a := range actions {
		if a.Action != "type" && a.Action != "wait" {
			onlyText = false
			break
		}
	}
	if onlyText {
		for _, a := range actions {
			if a.Action == "wait" {
				if err := sleepCtx(ctx, a.DurationMs); err != nil {
					return err
				}
				continue
			}
			if _, err := p.kvm.Do(ctx, http.MethodPost, "/api/hid/paste",
				map[string]any{"content": a.Text, "langue": ""}); err != nil {
				return err
			}
		}
		return nil
	}

	tok, err := p.kvm.Token(ctx)
	if err != nil {
		return err
	}
	c, _, err := websocket.Dial(ctx, p.kvm.WSURL(), &websocket.DialOptions{
		HTTPClient: p.kvm.HTTP(),
		HTTPHeader: http.Header{"Cookie": {"nano-kvm-token=" + tok}},
	})
	if err != nil {
		return err
	}
	defer func() { _ = c.Close(websocket.StatusNormalClosure, "") }()

	// Every message is a binary frame: one event byte, then a raw HID report.
	// The firmware switches on that first byte and drops what it cannot place,
	// silently — so a wrong encoding here reads as success all the way back to
	// the MCP client while nothing reaches the target.
	send := func(event byte, reports ...[]byte) error {
		for _, r := range reports {
			if err := c.Write(ctx, websocket.MessageBinary, hid.Frame(event, r)); err != nil {
				return err
			}
		}
		return nil
	}
	mouse := func(reports ...[]byte) error { return send(hid.EventMouse, reports...) }
	keyboard := func(reports ...[]byte) error { return send(hid.EventKeyboard, reports...) }

	for _, a := range actions {
		switch a.Action {
		case "wait":
			if err := sleepCtx(ctx, a.DurationMs); err != nil {
				return err
			}
		case "move":
			if a.X == nil || a.Y == nil {
				return fmt.Errorf("move requires x and y")
			}
			if err := mouse(hid.AbsoluteMouseReport(0, normToKVM(*a.X), normToKVM(*a.Y), 0)); err != nil {
				return err
			}
		case "click":
			btn := mouseButton[a.Button]
			if a.X != nil && a.Y != nil {
				// The absolute report carries the position alongside the
				// buttons, so the press and the release each name the point the
				// move went to.
				x, y := normToKVM(*a.X), normToKVM(*a.Y)
				if err := mouse(
					hid.AbsoluteMouseReport(0, x, y, 0),
					hid.AbsoluteMouseReport(btn, x, y, 0),
					hid.AbsoluteMouseReport(0, x, y, 0),
				); err != nil {
					return err
				}
				break
			}
			// No position given. The relative report has no position field, so
			// it presses wherever the cursor already is; an absolute report
			// would have to name a coordinate and would jump there first.
			if err := mouse(
				hid.RelativeMouseReport(btn, 0, 0, 0),
				hid.RelativeMouseReport(0, 0, 0, 0),
			); err != nil {
				return err
			}
		case "scroll":
			// Relative, so the wheel turns without moving the pointer.
			if err := mouse(hid.RelativeMouseReport(0, 0, 0, a.Amount)); err != nil {
				return err
			}
		case "type":
			for _, r := range a.Text {
				code, shift, ok := hid.CharCode(r)
				if !ok {
					continue
				}
				var mod byte
				if shift {
					mod = hid.ModShift
				}
				if err := keyboard(hid.KeyboardReport(mod, code), hid.KeyboardRelease()); err != nil {
					return err
				}
			}
		case "hotkey":
			// One bitmap byte for the modifiers, then the single non-modifier
			// key the tool contract allows.
			var mod, last byte
			for _, k := range a.Keys {
				switch k {
				case "ctrl":
					mod |= hid.ModCtrl
				case "shift":
					mod |= hid.ModShift
				case "alt":
					mod |= hid.ModAlt
				case "meta", "cmd", "win", "super":
					mod |= hid.ModMeta
				default:
					if code, ok := hid.Code(k); ok {
						last = code
					}
				}
			}
			if err := keyboard(hid.KeyboardReport(mod, last), hid.KeyboardRelease()); err != nil {
				return err
			}
		case "drag":
			if a.From == nil || a.To == nil ||
				a.From.X == nil || a.From.Y == nil ||
				a.To.X == nil || a.To.Y == nil {
				return fmt.Errorf("drag requires from and to")
			}
			// The whole drag stays on the absolute report. The firmware releases
			// the relative mouse the moment an absolute report arrives (and vice
			// versa), so a drag that pressed on one device and travelled on the
			// other would drop the button at the start point.
			btn := mouseButton[a.Button]
			fx, fy := normToKVM(*a.From.X), normToKVM(*a.From.Y)
			tx, ty := normToKVM(*a.To.X), normToKVM(*a.To.Y)
			if err := mouse(
				hid.AbsoluteMouseReport(0, fx, fy, 0),
				hid.AbsoluteMouseReport(btn, fx, fy, 0),
				hid.AbsoluteMouseReport(btn, tx, ty, 0),
				hid.AbsoluteMouseReport(0, tx, ty, 0),
			); err != nil {
				return err
			}
		}
	}
	return nil
}
