package backend

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/scgreenhalgh/nanokvm-mcp/internal/nanokvm"
)

func jpegOf(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var b bytes.Buffer
	_ = jpeg.Encode(&b, img, &jpeg.Options{Quality: 80})
	return b.Bytes()
}

func fakeStream(t *testing.T, frame []byte) *nanokvm.Client {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "nano-kvm-token", Value: "t"})
		w.Write([]byte(`{"code":0,"data":{"token":"t"}}`))
	})
	mux.HandleFunc("/api/stream/mjpeg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(frame)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return nanokvm.New(nanokvm.ClientConfig{BaseURL: srv.URL, Username: "a", Password: "b", HTTPClient: srv.Client()})
}

func TestPublicScreenshotSmallFramePassesThrough(t *testing.T) {
	frame := jpegOf(640, 480)
	p := NewPublic(fakeStream(t, frame))
	shot, err := p.Screenshot(context.Background(), ScreenshotOpts{})
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
	p := NewPublic(fakeStream(t, frame))
	shot, err := p.Screenshot(context.Background(), ScreenshotOpts{Width: 960})
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
	frame := jpegOf(3840, 2160)
	p := NewPublic(fakeStream(t, frame))
	_, err := p.Screenshot(context.Background(), ScreenshotOpts{Width: 1280})
	if err == nil {
		t.Fatal("expected refusal to decode an oversize frame")
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
