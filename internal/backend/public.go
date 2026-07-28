package backend

import (
	"bytes"
	"context"
	"encoding/json"
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
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
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
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
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

func normToKVM(v float64) int {
	k := int(v*0x7FFE) + 1
	if k < 1 {
		k = 1
	}
	if k > 0x7FFF {
		k = 0x7FFF
	}
	return k
}

var mouseButton = map[string]int{"left": 0, "middle": 1, "right": 2, "": 0}

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
				time.Sleep(time.Duration(a.DurationMs) * time.Millisecond)
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
	defer c.Close(websocket.StatusNormalClosure, "")

	send := func(msg []int) error {
		b, _ := json.Marshal(msg)
		return c.Write(ctx, websocket.MessageText, b)
	}

	for _, a := range actions {
		switch a.Action {
		case "wait":
			time.Sleep(time.Duration(a.DurationMs) * time.Millisecond)
		case "move":
			if a.X == nil || a.Y == nil {
				return fmt.Errorf("move requires x and y")
			}
			if err := send([]int{2, 3, 0, normToKVM(*a.X), normToKVM(*a.Y)}); err != nil {
				return err
			}
		case "click":
			btn := mouseButton[a.Button]
			if a.X != nil && a.Y != nil {
				if err := send([]int{2, 3, 0, normToKVM(*a.X), normToKVM(*a.Y)}); err != nil {
					return err
				}
			}
			if err := send([]int{2, 1, btn, 0, 0}); err != nil { // down
				return err
			}
			if err := send([]int{2, 2, 0, 0, 0}); err != nil { // up
				return err
			}
		case "scroll":
			if err := send([]int{2, 4, 0, 0, a.Amount}); err != nil {
				return err
			}
		case "type":
			for _, r := range a.Text {
				code, shift, ok := hid.CharCode(r)
				if !ok {
					continue
				}
				sh := 0
				if shift {
					sh = 2
				}
				if err := send([]int{1, int(code), 0, sh, 0, 0}); err != nil {
					return err
				}
				if err := send([]int{1, 0, 0, 0, 0, 0}); err != nil {
					return err
				}
			}
		case "hotkey":
			var mod int
			var last byte
			for _, k := range a.Keys {
				switch k {
				case "ctrl":
					mod |= 1
				case "shift":
					mod |= 2
				case "alt":
					mod |= 4
				case "meta", "cmd", "win", "super":
					mod |= 8
				default:
					if code, ok := hid.Code(k); ok {
						last = code
					}
				}
			}
			if err := send([]int{1, int(last), mod & 1, (mod >> 1) & 1, (mod >> 2) & 1, (mod >> 3) & 1}); err != nil {
				return err
			}
			if err := send([]int{1, 0, 0, 0, 0, 0}); err != nil {
				return err
			}
		case "drag":
			if a.From == nil || a.To == nil ||
				a.From.X == nil || a.From.Y == nil ||
				a.To.X == nil || a.To.Y == nil {
				return fmt.Errorf("drag requires from and to")
			}
			if err := send([]int{2, 3, 0, normToKVM(*a.From.X), normToKVM(*a.From.Y)}); err != nil {
				return err
			}
			if err := send([]int{2, 1, 0, 0, 0}); err != nil {
				return err
			}
			if err := send([]int{2, 3, 0, normToKVM(*a.To.X), normToKVM(*a.To.Y)}); err != nil {
				return err
			}
			if err := send([]int{2, 2, 0, 0, 0}); err != nil {
				return err
			}
		}
	}
	return nil
}
