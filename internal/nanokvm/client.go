package nanokvm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
)

var ErrAuth = errors.New("nanokvm: authentication failed")

type ClientConfig struct {
	BaseURL    string
	WSURL      string
	Username   string
	Password   string
	HTTPClient *http.Client
}

type Client struct {
	cfg       ClientConfig
	http      *http.Client
	mu        sync.Mutex
	token     string
	hwVersion string // cached hardware version, guarded by mu
}

func New(cfg ClientConfig) *Client {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	return &Client{cfg: cfg, http: hc}
}

type envelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// WSURL returns the configured websocket URL (used by backends).
func (c *Client) WSURL() string { return c.cfg.WSURL }

// BaseURL returns the configured base URL (used by the public backend).
func (c *Client) BaseURL() string { return c.cfg.BaseURL }

// HTTP returns the underlying HTTP client (used by the public backend).
func (c *Client) HTTP() *http.Client { return c.http }

// Token ensures a session token exists and returns it.
func (c *Client) Token(ctx context.Context) (string, error) {
	c.mu.Lock()
	tok := c.token
	c.mu.Unlock()
	if tok != "" {
		return tok, nil
	}
	if err := c.login(ctx); err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token, nil
}

func (c *Client) login(ctx context.Context) error {
	enc, err := EncryptPassword(c.cfg.Password)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"username": c.cfg.Username, "password": enc})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+"/api/auth/login", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("login: bad response: %w", err)
	}
	if env.Code != 0 {
		return fmt.Errorf("%w: %s", ErrAuth, env.Msg)
	}
	token := ""
	for _, ck := range resp.Cookies() {
		if ck.Name == "nano-kvm-token" {
			token = ck.Value
		}
	}
	if token == "" {
		var d struct {
			Token string `json:"token"`
		}
		_ = json.Unmarshal(env.Data, &d)
		token = d.Token
	}
	c.mu.Lock()
	c.token = token
	c.mu.Unlock()
	return nil
}

// Do performs an authenticated request, re-authenticating once on auth failure,
// and returns the response's `data` field.
func (c *Client) Do(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	data, err := c.do(ctx, method, path, body)
	if errors.Is(err, ErrAuth) {
		c.mu.Lock()
		c.token = ""
		c.mu.Unlock()
		return c.do(ctx, method, path, body)
	}
	return data, err
}

func (c *Client) do(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	tok, err := c.Token(ctx)
	if err != nil {
		return nil, err
	}
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.BaseURL+path, r)
	if err != nil {
		return nil, err
	}
	req.AddCookie(&http.Cookie{Name: "nano-kvm-token", Value: tok})
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrAuth
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("nanokvm: %s %s: HTTP %d", method, path, resp.StatusCode)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("nanokvm: %s %s: bad response: %w", method, path, err)
	}
	if env.Code == -2 {
		return nil, ErrAuth
	}
	if env.Code != 0 {
		return nil, fmt.Errorf("nanokvm: %s %s: %s", method, path, env.Msg)
	}
	return env.Data, nil
}
