package nanokvm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
)

// fakeKVM is an httptest server implementing the subset of the NanoKVM API the
// client depends on, with the real response envelope and status codes.
type fakeKVM struct {
	*httptest.Server
	mu         sync.Mutex
	token      string
	loginCalls int
	expireOnce bool                         // if true, the first authed request returns unauthenticated
	handlers   map[string]func() (any, int) // path -> (data, code)
}

func newFakeKVM() *fakeKVM {
	f := &fakeKVM{token: "session-abc", handlers: map[string]func() (any, int){}}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.loginCalls++
		f.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "nano-kvm-token", Value: f.token})
		writeEnv(w, 0, "ok", map[string]string{"token": f.token})
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Auth gate: require the session cookie.
		ck, err := r.Cookie("nano-kvm-token")
		if err != nil || ck.Value != f.token {
			w.WriteHeader(http.StatusUnauthorized)
			writeEnv(w, -2, "unauthorized", nil)
			return
		}
		f.mu.Lock()
		expire := f.expireOnce
		f.expireOnce = false
		h := f.handlers[r.URL.Path]
		f.mu.Unlock()
		if expire {
			w.WriteHeader(http.StatusUnauthorized)
			writeEnv(w, -2, "expired", nil)
			return
		}
		if h == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		data, code := h()
		writeEnv(w, code, "ok", data)
	})

	f.Server = httptest.NewServer(mux)
	return f
}

func writeEnv(w http.ResponseWriter, code int, msg string, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "msg": msg, "data": data})
}

func (f *fakeKVM) on(path string, data any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[path] = func() (any, int) { return data, 0 }
}
