package backend

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/tylervick/nanokvm-mcp/internal/nanokvm"
)

func fakeWSKVM(t *testing.T, recv *[][]int) *nanokvm.Client {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "nano-kvm-token", Value: "t"})
		w.Write([]byte(`{"code":0,"data":{"token":"t"}}`))
	})
	mux.HandleFunc("/api/hid/paste", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"msg":"ok"}`))
	})
	mux.HandleFunc("/api/ws", func(w http.ResponseWriter, r *http.Request) {
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
			var msg []int
			if json.Unmarshal(data, &msg) == nil {
				*recv = append(*recv, msg)
			}
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return nanokvm.New(nanokvm.ClientConfig{
		BaseURL: srv.URL, WSURL: "ws" + srv.URL[len("http"):] + "/api/ws",
		Username: "a", Password: "b", HTTPClient: srv.Client(),
	})
}

func TestPublicInputClickSendsWSMessages(t *testing.T) {
	var recv [][]int
	p := NewPublic(fakeWSKVM(t, &recv))
	x, y := 0.5, 0.5
	err := p.Input(context.Background(), []Action{{Action: "click", X: &x, Y: &y, Button: "left"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(recv) == 0 {
		t.Fatal("expected websocket messages for a click")
	}
	// A click issues at least a down and an up event (type 2).
	if recv[0][0] != 2 {
		t.Errorf("expected mouse event type 2, got %v", recv[0])
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

func TestPublicInputRejectsMalformedCoords(t *testing.T) {
	var recv [][]int
	p := NewPublic(fakeWSKVM(t, &recv))
	if err := p.Input(context.Background(), []Action{{Action: "move"}}); err == nil {
		t.Error("move without coords should return an error, not panic")
	}
	if err := p.Input(context.Background(), []Action{{Action: "drag", From: &Point{}, To: &Point{}}}); err == nil {
		t.Error("drag with empty points should return an error, not panic")
	}
}
