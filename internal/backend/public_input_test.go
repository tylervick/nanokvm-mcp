package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/coder/websocket"
	"github.com/scgreenhalgh/nanokvm-mcp/internal/nanokvm"
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
