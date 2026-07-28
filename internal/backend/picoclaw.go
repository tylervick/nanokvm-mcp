package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

//nolint:gosec // G101: header and path names for the firmware's token, not credentials.
const (
	InternalTokenHeader = "X-NanoKVM-Internal-Token"
	SessionIDHeader     = "X-PicoClaw-Session-ID"
	DefaultTokenPath    = "/etc/kvm/.picoclaw_internal_token"
)

type Picoclaw struct {
	baseURL   string
	token     string
	sessionID string
	http      *http.Client
}

func NewPicoclaw(baseURL, token, sessionID string, hc *http.Client) *Picoclaw {
	if hc == nil {
		hc = &http.Client{}
	}
	return &Picoclaw{baseURL: strings.TrimRight(baseURL, "/"), token: token, sessionID: sessionID, http: hc}
}

func ReadInternalToken(path string) (string, error) {
	b, err := os.ReadFile(path) //nolint:gosec // G304: path is operator-configured; default is the firmware's own token file.
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func (p *Picoclaw) Name() string { return "picoclaw" }

func (p *Picoclaw) headers(req *http.Request) {
	req.Header.Set(InternalTokenHeader, p.token)
	req.Header.Set(SessionIDHeader, p.sessionID)
}

func (p *Picoclaw) Screenshot(ctx context.Context, opts ScreenshotOpts) (Shot, error) {
	// Omit format=base64 to get raw image/jpeg bytes: no base64, no decode.
	q := url.Values{}
	if opts.Width > 0 {
		q.Set("width", strconv.Itoa(opts.Width))
	}
	if opts.Height > 0 {
		q.Set("height", strconv.Itoa(opts.Height))
	}
	if opts.Quality > 0 {
		q.Set("quality", strconv.Itoa(opts.Quality))
	}
	u := p.baseURL + "/api/picoclaw/screenshot"
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Shot{}, err
	}
	p.headers(req)
	resp, err := p.http.Do(req)
	if err != nil {
		return Shot{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Shot{}, fmt.Errorf("picoclaw screenshot: HTTP %d", resp.StatusCode)
	}
	jpeg, err := io.ReadAll(resp.Body)
	if err != nil {
		return Shot{}, err
	}
	return Shot{JPEG: jpeg}, nil
}

func (p *Picoclaw) Input(ctx context.Context, actions []Action) error {
	if err := ValidateActions(actions); err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{"actions": actions})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/picoclaw/actions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	p.headers(req)
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("picoclaw actions: HTTP %d: %s", resp.StatusCode, raw)
	}
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("picoclaw actions: bad response: %w", err)
	}
	if env.Code != 0 {
		return fmt.Errorf("picoclaw actions: %s", env.Msg)
	}
	return nil
}
