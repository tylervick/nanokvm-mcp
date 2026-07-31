package nanokvm

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// fakeKVM is an httptest server implementing the subset of the NanoKVM API the
// client depends on, with the real response envelope and status codes.
type fakeKVM struct {
	*httptest.Server
	mu         sync.Mutex
	token      string
	loginCalls int
	expireOnce bool // if true, the first authed request returns unauthenticated
	// handlers maps a path to its response. The request is passed through so a
	// handler can apply upstream's own acceptance rule to the body.
	handlers map[string]func(*http.Request) (any, int)
	bodies   map[string][]byte // path -> the last request body received
}

func newFakeKVM() *fakeKVM {
	f := &fakeKVM{
		token:    "session-abc",
		handlers: map[string]func(*http.Request) (any, int){},
		bodies:   map[string][]byte{},
	}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.loginCalls++
		h := f.handlers[r.URL.Path]
		f.mu.Unlock()
		f.recordBody(r)
		// An onFunc override runs for its side effects (e.g. latency) before the
		// canonical login response; the login envelope itself stays fixed.
		if h != nil {
			h(r)
		}
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
		f.recordBody(r)
		data, code := h(r)
		writeEnv(w, code, "ok", data)
	})

	f.Server = httptest.NewServer(mux)
	return f
}

func writeEnv(w http.ResponseWriter, code int, msg string, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "msg": msg, "data": data})
}

// recordBody keeps the raw request body so a test can assert on the exact keys
// that went over the wire, not just on the values our own decoder recovers. The
// body is put back so the handler still sees it.
func (f *fakeKVM) recordBody(r *http.Request) {
	if r.Body == nil {
		return
	}
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(b))
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bodies[r.URL.Path] = b
}

func (f *fakeKVM) on(path string, data any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[path] = func(*http.Request) (any, int) { return data, 0 }
}

func (f *fakeKVM) onFunc(path string, h func() (any, int)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[path] = func(*http.Request) (any, int) { return h() }
}

// onRequest registers a handler that sees the request, so it can reject a body
// the firmware would reject.
func (f *fakeKVM) onRequest(path string, h func(*http.Request) (any, int)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[path] = h
}

// body returns the raw JSON body last sent to path.
func (f *fakeKVM) body(t *testing.T, path string) map[string]any {
	t.Helper()
	f.mu.Lock()
	raw := f.bodies[path]
	f.mu.Unlock()
	if len(raw) == 0 {
		t.Fatalf("no request body was sent to %s", path)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("body sent to %s is not a JSON object: %s", path, raw)
	}
	return m
}
