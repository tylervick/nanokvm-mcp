package nanokvm

import (
	"context"
	"net/http"
	"testing"
)

func newTestClient(f *fakeKVM) *Client {
	return New(ClientConfig{
		BaseURL:    f.URL,
		Username:   "admin",
		Password:   "admin",
		HTTPClient: f.Client(),
	})
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
