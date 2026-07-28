package backend

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakePicoclaw(t *testing.T, wantToken string) (*httptest.Server, *[]byte) {
	var lastBody []byte
	mux := http.NewServeMux()
	mux.HandleFunc("/api/picoclaw/screenshot", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(InternalTokenHeader) != wantToken || r.Header.Get(SessionIDHeader) == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte{0xFF, 0xD8, 0xFF, 0xD9}) // minimal JPEG SOI+EOI
	})
	mux.HandleFunc("/api/picoclaw/actions", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(InternalTokenHeader) != wantToken || r.Header.Get(SessionIDHeader) == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		lastBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"code":0,"msg":"ok","data":{"executed_actions":1}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &lastBody
}

func TestPicoclawScreenshotPassesThroughBytes(t *testing.T) {
	srv, _ := fakePicoclaw(t, "tok")
	p := NewPicoclaw(srv.URL, "tok", "sess-1", srv.Client())
	shot, err := p.Screenshot(context.Background(), ScreenshotOpts{Width: 960})
	if err != nil {
		t.Fatal(err)
	}
	if len(shot.JPEG) != 4 || shot.JPEG[0] != 0xFF {
		t.Errorf("expected raw jpeg bytes passed through, got %v", shot.JPEG)
	}
}

func TestPicoclawScreenshotForcesFreshFrame(t *testing.T) {
	// picoclaw serves a CACHED frame when width, height, and quality are all unset.
	// With no ScreenshotOpts, our backend must still bust that cache (send a quality)
	// so an MCP screenshot reflects the live screen.
	var gotQuery string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/picoclaw/screenshot", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte{0xFF, 0xD8, 0xFF, 0xD9})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	p := NewPicoclaw(srv.URL, "tok", "sess-1", srv.Client())
	if _, err := p.Screenshot(context.Background(), ScreenshotOpts{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotQuery, "quality=") {
		t.Errorf("no-opts screenshot must send a cache-busting quality param, got query %q", gotQuery)
	}
	if strings.Contains(gotQuery, "format=base64") {
		t.Errorf("screenshot must not request base64 (raw passthrough), got query %q", gotQuery)
	}
}

func TestPicoclawInputValidatesAndSends(t *testing.T) {
	srv, body := fakePicoclaw(t, "tok")
	p := NewPicoclaw(srv.URL, "tok", "sess-1", srv.Client())
	err := p.Input(context.Background(), []Action{{Action: "type", Text: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(*body) == 0 {
		t.Error("expected actions body to be POSTed")
	}
	// Invalid actions must be rejected before any network call.
	if err := p.Input(context.Background(), []Action{{Action: "nope"}}); err == nil {
		t.Error("invalid action should be rejected")
	}
}

func TestPicoclawAuthRejected(t *testing.T) {
	srv, _ := fakePicoclaw(t, "tok")
	p := NewPicoclaw(srv.URL, "wrong", "sess-1", srv.Client())
	if _, err := p.Screenshot(context.Background(), ScreenshotOpts{}); err == nil {
		t.Error("expected auth failure with wrong token")
	}
}
