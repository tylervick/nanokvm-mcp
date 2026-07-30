package backend

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tylervick/nanokvm-mcp/internal/nanokvm"
)

func jpegOf(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var b bytes.Buffer
	_ = jpeg.Encode(&b, img, &jpeg.Options{Quality: 80})
	return b.Bytes()
}

// frameInterval paces the fake capture. The real one runs at the configured
// FPS; what matters here is only that the connection stays open between
// frames, with no EOF for a reader to wait on.
const frameInterval = 25 * time.Millisecond

// mjpegFake serves /api/stream/mjpeg the way upstream's mjpeg.Connect does: a
// multipart/x-mixed-replace body with one part per captured frame, held open
// until the client disconnects.
//
// The absence of an EOF is the point. A fake that writes one JPEG and returns
// lets a read-the-whole-body screenshot look correct, because the close it
// depends on only ever comes from the fake.
type mjpegFake struct {
	srv *httptest.Server
	wg  sync.WaitGroup

	mu      sync.Mutex
	written int
}

func newMJPEGFake(t *testing.T, frames ...[]byte) *mjpegFake {
	t.Helper()
	m := &mjpegFake{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "nano-kvm-token", Value: "t"})
		_, _ = w.Write([]byte(`{"code":0,"data":{"token":"t"}}`))
	})
	mux.HandleFunc("/api/stream/mjpeg", func(w http.ResponseWriter, r *http.Request) {
		m.wg.Add(1)
		defer m.wg.Done()

		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("ResponseWriter cannot flush; the fake cannot stream")
			return
		}
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
		for i := 0; ; i++ {
			frame := frames[i%len(frames)]
			hdr := fmt.Sprintf("--frame\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(frame))
			if _, err := io.WriteString(w, hdr); err != nil {
				return
			}
			if _, err := w.Write(frame); err != nil {
				return
			}
			if _, err := io.WriteString(w, "\r\n"); err != nil {
				return
			}
			flusher.Flush()

			m.mu.Lock()
			m.written++
			m.mu.Unlock()

			select {
			case <-r.Context().Done(): // the client hung up
				return
			case <-time.After(frameInterval):
			}
		}
	})
	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mjpegFake) client() *nanokvm.Client {
	return nanokvm.New(nanokvm.ClientConfig{
		BaseURL: m.srv.URL, Username: "a", Password: "b", HTTPClient: m.srv.Client(),
	})
}

func (m *mjpegFake) public() *Public { return NewPublic(m.client()) }

// framesWritten waits for the stream handler to notice the disconnect, then
// reports how many parts it managed to write.
func (m *mjpegFake) framesWritten(t *testing.T) int {
	t.Helper()
	m.wg.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.written
}

// screenshotCtx bounds every screenshot test. Reading the stream to EOF cannot
// finish, so a regression fails here on the deadline instead of hanging.
func screenshotCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// fakeStream serves a bare JPEG body with no multipart framing, for the
// fallback path.
func fakeStream(t *testing.T, frame []byte) *nanokvm.Client {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "nano-kvm-token", Value: "t"})
		_, _ = w.Write([]byte(`{"code":0,"data":{"token":"t"}}`))
	})
	mux.HandleFunc("/api/stream/mjpeg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(frame)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return nanokvm.New(nanokvm.ClientConfig{BaseURL: srv.URL, Username: "a", Password: "b", HTTPClient: srv.Client()})
}

func TestPublicScreenshotTakesTheFirstFrameOfTheStream(t *testing.T) {
	first, second := jpegOf(640, 480), jpegOf(320, 240)
	m := newMJPEGFake(t, first, second)

	shot, err := m.public().Screenshot(screenshotCtx(t), ScreenshotOpts{})
	if err != nil {
		t.Fatalf("Screenshot: %v\n(the stream has no EOF; reading the body whole waits for the byte cap)", err)
	}
	if !bytes.Equal(shot.JPEG, first) {
		t.Errorf("got %d bytes, want the stream's first frame (%d bytes)", len(shot.JPEG), len(first))
	}
	if shot.Width != 640 || shot.Height != 480 {
		t.Errorf("want 640x480 reported, got %dx%d", shot.Width, shot.Height)
	}
}

func TestPublicScreenshotStopsAfterTheFirstFrame(t *testing.T) {
	// One screenshot must cost one frame. Draining to the 8 MB cap would
	// buffer megabytes and hold the capture open, on a device with tens of
	// megabytes free.
	m := newMJPEGFake(t, jpegOf(640, 480))
	if _, err := m.public().Screenshot(screenshotCtx(t), ScreenshotOpts{}); err != nil {
		t.Fatal(err)
	}
	if got := m.framesWritten(t); got > 3 {
		t.Errorf("the capture wrote %d frames for one screenshot; take one and hang up", got)
	}
}

func TestPublicScreenshotSmallFramePassesThrough(t *testing.T) {
	frame := jpegOf(640, 480)
	m := newMJPEGFake(t, frame)

	shot, err := m.public().Screenshot(screenshotCtx(t), ScreenshotOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := image.Decode(bytes.NewReader(shot.JPEG)); err != nil {
		t.Fatalf("returned bytes not a valid image: %v", err)
	}
	if !bytes.Equal(shot.JPEG, frame) {
		t.Error("small frame should pass through unchanged (no re-encode)")
	}
	if shot.Width != 640 || shot.Height != 480 {
		t.Errorf("want 640x480 reported, got %dx%d", shot.Width, shot.Height)
	}
}

func TestPublicScreenshotResizesWithinCap(t *testing.T) {
	frame := jpegOf(1920, 1080) // 2,073,600 px, under the 2,100,000 cap
	m := newMJPEGFake(t, frame)

	shot, err := m.public().Screenshot(screenshotCtx(t), ScreenshotOpts{Width: 960})
	if err != nil {
		t.Fatalf("resize within cap should succeed: %v", err)
	}
	if shot.Width != 960 {
		t.Errorf("want width 960, got %d", shot.Width)
	}
	if shot.Height != 540 { // 1080 * 960/1920
		t.Errorf("want height 540 (aspect preserved), got %d", shot.Height)
	}
	if _, _, err := image.Decode(bytes.NewReader(shot.JPEG)); err != nil {
		t.Fatalf("resized output not a valid image: %v", err)
	}
}

func TestPublicScreenshotRefusesOversizeDecode(t *testing.T) {
	// A 4K frame exceeds maxDecodePixels; must be refused, not decoded.
	m := newMJPEGFake(t, jpegOf(3840, 2160))
	_, err := m.public().Screenshot(screenshotCtx(t), ScreenshotOpts{Width: 1280})
	if err == nil {
		t.Fatal("expected refusal to decode an oversize frame")
	}
	// Name the refusal, so this cannot pass on a timeout or a transport error.
	if !strings.Contains(err.Error(), "exceeds decode cap") {
		t.Errorf("want the decode-cap refusal, got %v", err)
	}
}

func TestPublicScreenshotAcceptsABareJPEGBody(t *testing.T) {
	// Fallback for anything that answers with a plain image rather than the
	// multipart stream: the body ends, so reading it whole is correct there.
	frame := jpegOf(640, 480)
	p := NewPublic(fakeStream(t, frame))

	shot, err := p.Screenshot(screenshotCtx(t), ScreenshotOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(shot.JPEG, frame) {
		t.Error("bare JPEG body should pass through unchanged")
	}
}

func TestExtractJPEGErrors(t *testing.T) {
	if _, err := extractJPEG([]byte{0x00, 0x01, 0x02}); err == nil {
		t.Error("missing SOI marker should error")
	}
	if _, err := extractJPEG([]byte{0xFF, 0xD8, 0x11, 0x22}); err == nil {
		t.Error("missing EOI marker should error")
	}
	// A well-formed minimal SOI...EOI should extract cleanly: SOI (2 bytes) +
	// one data byte + EOI (2 bytes) = 5 bytes, trailing 0x99 excluded.
	got, err := extractJPEG([]byte{0x00, 0xFF, 0xD8, 0x42, 0xFF, 0xD9, 0x99})
	if err != nil {
		t.Fatalf("valid markers should extract: %v", err)
	}
	want := []byte{0xFF, 0xD8, 0x42, 0xFF, 0xD9}
	if !bytes.Equal(got, want) {
		t.Errorf("expected the SOI..EOI slice %v, got %v", want, got)
	}
}
