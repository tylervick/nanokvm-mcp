package nanokvm

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"
)

func newTestClient(f *fakeKVM) *Client {
	return New(ClientConfig{
		BaseURL:    f.URL,
		Username:   "admin",
		Password:   "admin",
		HTTPClient: f.Client(),
	})
}

// Upstream's proto.LoginReq has no json tags; gin binds it with encoding/json,
// which matches "username"/"password" against Username/Password
// case-insensitively. The password travels as the CryptoJS container the
// firmware expects, never as plaintext.
func TestLoginSendsTheFieldsUpstreamBinds(t *testing.T) {
	f := newFakeKVM()
	defer f.Close()
	c := newTestClient(f)

	if _, err := c.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	body := f.body(t, "/api/auth/login")
	if body["username"] != "admin" {
		t.Errorf("username = %v, want admin", body["username"])
	}
	pw, _ := body["password"].(string)
	if pw == "" || pw == "admin" {
		t.Errorf("password = %q, want the encrypted container rather than the plaintext", pw)
	}
}

func TestDoAuthenticatesOnce(t *testing.T) {
	f := newFakeKVM()
	defer f.Close()
	f.on("/api/vm/info", map[string]string{"ip": "10.0.0.5"})
	c := newTestClient(f)

	raw, err := c.Do(context.Background(), http.MethodGet, "/api/vm/info", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" || f.loginCalls != 1 {
		t.Errorf("want one login and data, got logins=%d data=%s", f.loginCalls, raw)
	}

	// Second call reuses the token: still exactly one login.
	if _, err := c.Do(context.Background(), http.MethodGet, "/api/vm/info", nil); err != nil {
		t.Fatal(err)
	}
	if f.loginCalls != 1 {
		t.Errorf("token should be reused, got logins=%d", f.loginCalls)
	}
}

func TestTokenLogsInOnceUnderConcurrentFirstUse(t *testing.T) {
	f := newFakeKVM()
	defer f.Close()
	// A slow login holds the first caller inside login() long enough that every
	// other goroutine reaches its empty-token check while that login is still
	// in flight — the exact window that spawns duplicate firmware sessions.
	f.onFunc("/api/auth/login", func() (any, int) {
		time.Sleep(100 * time.Millisecond)
		return nil, 0
	})
	c := newTestClient(f)

	const goroutines = 8
	start := make(chan struct{})
	tokens := make([]string, goroutines)
	errs := make([]error, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release everyone into Token() together
			tokens[i], errs[i] = c.Token(context.Background())
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
		if tokens[i] != tokens[0] || tokens[i] == "" {
			t.Errorf("goroutine %d got token %q, want the shared token %q", i, tokens[i], tokens[0])
		}
	}
	if f.loginCalls != 1 {
		t.Errorf("concurrent first use must create one firmware session, got logins=%d", f.loginCalls)
	}
}

func TestDoReauthsOnExpiry(t *testing.T) {
	f := newFakeKVM()
	defer f.Close()
	f.on("/api/vm/info", map[string]string{"ip": "10.0.0.5"})
	c := newTestClient(f)

	// Prime a token.
	if _, err := c.Do(context.Background(), http.MethodGet, "/api/vm/info", nil); err != nil {
		t.Fatal(err)
	}
	// Force the next request to look expired; the client must re-login and retry.
	f.mu.Lock()
	f.expireOnce = true
	f.mu.Unlock()

	if _, err := c.Do(context.Background(), http.MethodGet, "/api/vm/info", nil); err != nil {
		t.Fatalf("expected transparent re-auth, got %v", err)
	}
	if f.loginCalls != 2 {
		t.Errorf("want re-login on expiry, got logins=%d", f.loginCalls)
	}
}
