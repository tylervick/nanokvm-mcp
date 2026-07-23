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
