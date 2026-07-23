# NanoKVM MCP Sidecar Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Go MCP server that runs on a Sipeed NanoKVM device and exposes its power, HID input, screen capture, and ISO-mounting functions to MCP clients over authenticated HTTP.

**Architecture:** A standalone static `riscv64` daemon that is a pure HTTP client of the stock NanoKVM firmware server on `127.0.0.1`. Screen capture and HID input go through a `KVMBackend` interface with a preferred on-device implementation (`picoclaw`, using the firmware's internal loopback API) and a fallback (`public`, using the public REST/WebSocket API). All other capabilities use the public REST API directly. The MCP endpoint is served over streamable HTTP with a bearer token.

**Tech Stack:** Go 1.25.4, `github.com/modelcontextprotocol/go-sdk` v1.6.1, standard library (`net/http`, `image/jpeg`, `crypto/*`), `mise` for the toolchain. No CGO.

## Global Constraints

Copied verbatim from the design spec (`docs/superpowers/specs/2026-07-22-nanokvm-mcp-sidecar-design.md`). Every task's requirements implicitly include these.

- **License:** GPL-3.0. A `LICENSE` file with the full text ships in the repo root. Any file containing code ported from `sipeed/NanoKVM` carries a header noting origin.
- **Build:** `CGO_ENABLED=0 GOOS=linux GOARCH=riscv64 go build -trimpath -ldflags="-s -w"`. No custom toolchain, no `patchelf`.
- **Binary budget:** under 15 MB (measured 7.1 MB for a minimal server). Enforced in CI.
- **Resident budget:** under 25 MB, against 43 MB measured free on-device. Enforced in the device smoke test.
- **Memory rule (binding):** the `picoclaw` backend MUST NOT decode JPEG — it passes bytes through. Only `public` decodes, and it MUST hard-cap decode resolution so a 4K frame (~33 MB RGBA) can never be decoded.
- **No mocked transport in tests.** Tests run against an `httptest` fake NanoKVM with real endpoint shapes and status codes.
- **Default bind:** `127.0.0.1:8080`. Network exposure requires explicit configuration.
- **Go module path:** `github.com/scgreenhalgh/nanokvm-mcp` (adjust to the final repo owner before first push; used consistently below).
- **Resolved upstream constants:** `sessionIDHeader = "X-PicoClaw-Session-ID"`; internal token header `X-NanoKVM-Internal-Token`; token file `/etc/kvm/.picoclaw_internal_token`; `AppDir=/kvmapp`, `BackupDir=/root/old`, `CacheDir=/root/.kvmcache`.
- **Install layout:** binary + config in `/root/nanokvm-mcp/`; audit log in `/data/nanokvm-mcp/`; init script `/etc/init.d/S96nanokvm-mcp`.
- **Standard firmware response envelope:** `{"code": int, "msg": string, "data": <any>}` where `code == 0` means success.

---

## File Structure

```
go.mod, go.sum
mise.toml                              # toolchain pin + tasks
LICENSE                                # GPL-3.0 full text
README.md
.github/workflows/ci.yml               # build (riscv64), size gate, test, apicheck
cmd/nanokvm-mcp/main.go                # config load, wiring, serve. No logic.
internal/config/config.go             # env + file config, token generation
internal/nanokvm/auth.go              # CryptoJS-compatible AES password encryption (ported; GPL header)
internal/nanokvm/client.go            # public REST client: login, request, re-auth on 401
internal/nanokvm/vm.go                # power, power_cycle, led, hdmi, info, hardware
internal/nanokvm/storage.go           # image list/mount/unmount/mounted
internal/nanokvm/fake_test.go         # httptest fake NanoKVM shared by client tests
internal/backend/backend.go           # KVMBackend interface, Action, ScreenshotOpts, results
internal/backend/picoclaw.go          # picoclawBackend (preferred)
internal/backend/public.go            # publicBackend (fallback): mjpeg screenshot + WS/paste input
internal/backend/select.go            # startup probe + selection
internal/hid/keycodes.go              # USB HID keycode tables (derived from USB HUT spec)
internal/audit/audit.go               # mutating-call audit log with text redaction
internal/httpauth/bearer.go           # bearer-token middleware
internal/mcpserver/server.go          # server construction, tool registration, read-only filter
internal/mcpserver/tools.go           # tool handlers (read-only + mutating)
tools/apicheck/apicheck_test.go       # upstream route drift detector
deploy/S96nanokvm-mcp                 # init script
deploy/install.sh                     # install script
```

---

## Phase 0 — Foundation

### Task 1: Project scaffold, toolchain, license, CI, riscv64 build gate

**Files:**
- Create: `go.mod`, `mise.toml`, `LICENSE`, `.github/workflows/ci.yml`, `cmd/nanokvm-mcp/main.go`

**Interfaces:**
- Consumes: nothing.
- Produces: a buildable module `github.com/scgreenhalgh/nanokvm-mcp`; `mise run build` producing `dist/nanokvm-mcp`; `mise run sizecheck`.

- [ ] **Step 1: Create the Go module and dependency**

```bash
cd /Users/tyler/orca/workspaces/nanokvm-mcp/port-and-update-nanokvm-mcp
go mod init github.com/scgreenhalgh/nanokvm-mcp
go get github.com/modelcontextprotocol/go-sdk@v1.6.1
```

Expected: `go.mod` names the module and requires the SDK at v1.6.1; `go.sum` is written.

- [ ] **Step 2: Write `mise.toml`**

```toml
[tools]
go = "1.25.4"

[env]
CGO_ENABLED = "0"

[tasks.build]
run = "GOOS=linux GOARCH=riscv64 go build -trimpath -ldflags='-s -w' -o dist/nanokvm-mcp ./cmd/nanokvm-mcp"

[tasks.build-host]
run = "go build -trimpath -ldflags='-s -w' -o dist/nanokvm-mcp-host ./cmd/nanokvm-mcp"

[tasks.test]
run = "go test ./..."

[tasks.apicheck]
run = "go test ./tools/apicheck/..."

[tasks.sizecheck]
depends = ["build"]
run = "sz=$(wc -c < dist/nanokvm-mcp); echo \"binary: $sz bytes\"; test \"$sz\" -lt 15728640 || { echo 'FAIL: binary exceeds 15MB'; exit 1; }"
```

- [ ] **Step 3: Add the GPL-3.0 LICENSE file**

```bash
curl -fsSL https://www.gnu.org/licenses/gpl-3.0.txt -o LICENSE
head -1 LICENSE   # expect: "                    GNU GENERAL PUBLIC LICENSE"
```

- [ ] **Step 4: Write a minimal `cmd/nanokvm-mcp/main.go` that builds**

```go
// Command nanokvm-mcp is an MCP server for Sipeed NanoKVM devices.
package main

import (
	"fmt"
	"os"
)

var version = "dev"

func main() {
	fmt.Fprintf(os.Stderr, "nanokvm-mcp %s\n", version)
}
```

- [ ] **Step 5: Verify the riscv64 build and size gate pass**

Run: `mise run sizecheck`
Expected: prints a byte count well under 15728640 and exits 0. Confirms `file dist/nanokvm-mcp` is a statically linked riscv64 ELF (spot-check with `file dist/nanokvm-mcp`).

- [ ] **Step 6: Write `.github/workflows/ci.yml`**

```yaml
name: ci
on: [push, pull_request]
jobs:
  build-test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: jdx/mise-action@v2
      - run: mise run test
      - run: mise run sizecheck
      - run: mise run apicheck
```

Note: `mise run apicheck` will fail until Task 14 creates `tools/apicheck`. That is acceptable within the phase; the gate is green by end of plan. If running CI before Task 14, comment out that line and restore it in Task 14.

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum mise.toml LICENSE .github/workflows/ci.yml cmd/nanokvm-mcp/main.go
git commit -m "chore: scaffold Go module, mise toolchain, GPL license, riscv64 build gate"
```

---

## Phase 1 — NanoKVM public REST client

### Task 2: Config and token store

**Files:**
- Create: `internal/config/config.go`, `internal/config/config_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Config struct { Host string; Username string; Password string; UseHTTPS bool; VerifySSL bool; BindAddr string; ReadOnly bool; AuditPath string; AuditFull bool; BearerToken string }`
  - `func Load() (Config, error)` — reads env vars, applies defaults, loads-or-generates the bearer token.
  - `func GenerateToken() (string, error)` — 32 random bytes, base64url, no padding.

- [ ] **Step 1: Write the failing test**

```go
package config

import (
	"os"
	"strings"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	os.Clearenv()
	os.Setenv("NANOKVM_HOST", "127.0.0.1")
	os.Setenv("NANOKVM_MCP_TOKEN", "fixed-token")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Username != "admin" || c.Password != "admin" {
		t.Errorf("want admin/admin, got %q/%q", c.Username, c.Password)
	}
	if c.BindAddr != "127.0.0.1:8080" {
		t.Errorf("want default bind 127.0.0.1:8080, got %q", c.BindAddr)
	}
	if c.VerifySSL != true {
		t.Errorf("VerifySSL should default true")
	}
	if c.ReadOnly != false {
		t.Errorf("ReadOnly should default false")
	}
	if c.BearerToken != "fixed-token" {
		t.Errorf("token from env not honored")
	}
}

func TestLoadRequiresHost(t *testing.T) {
	os.Clearenv()
	os.Setenv("NANOKVM_MCP_TOKEN", "x")
	if _, err := Load(); err == nil {
		t.Fatal("expected error when NANOKVM_HOST unset")
	}
}

func TestGenerateTokenUnique(t *testing.T) {
	a, _ := GenerateToken()
	b, _ := GenerateToken()
	if a == b || len(a) < 40 || strings.ContainsAny(a, "+/=") {
		t.Errorf("tokens should be unique, long, url-safe: %q %q", a, b)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/config/`
Expected: FAIL (package/functions not defined).

- [ ] **Step 3: Implement `internal/config/config.go`**

```go
// Package config loads sidecar configuration from environment variables.
package config

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"strings"
)

type Config struct {
	Host        string
	Username    string
	Password    string
	UseHTTPS    bool
	VerifySSL   bool
	BindAddr    string
	ReadOnly    bool
	AuditPath   string
	AuditFull   bool
	BearerToken string
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func boolEnv(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	return strings.EqualFold(v, "true") || v == "1"
}

// GenerateToken returns 32 random bytes as unpadded base64url.
func GenerateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Load reads configuration from the environment and applies defaults.
func Load() (Config, error) {
	c := Config{
		Host:      os.Getenv("NANOKVM_HOST"),
		Username:  env("NANOKVM_USER", "admin"),
		Password:  env("NANOKVM_PASS", "admin"),
		UseHTTPS:  boolEnv("NANOKVM_HTTPS", false),
		VerifySSL: boolEnv("NANOKVM_VERIFY_SSL", true),
		BindAddr:  env("NANOKVM_MCP_BIND", "127.0.0.1:8080"),
		ReadOnly:  boolEnv("NANOKVM_MCP_READONLY", false),
		AuditPath: env("NANOKVM_MCP_AUDIT", "/data/nanokvm-mcp/audit.log"),
		AuditFull: boolEnv("NANOKVM_MCP_AUDIT_FULL", false),
	}
	if c.Host == "" {
		return Config{}, errors.New("NANOKVM_HOST is required")
	}
	c.BearerToken = os.Getenv("NANOKVM_MCP_TOKEN")
	if c.BearerToken == "" {
		t, err := GenerateToken()
		if err != nil {
			return Config{}, err
		}
		c.BearerToken = t
	}
	return c, nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/config/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/
git commit -m "feat(config): env-driven config with bearer token generation"
```

---

### Task 3: CryptoJS-compatible password encryption

Ported logic from `sipeed/NanoKVM` frontend + the existing Python fork's `auth.py`. The file carries a GPL origin header. Correctness is verified against a fixed known-answer vector produced with OpenSSL (below), not against a mock.

**Files:**
- Create: `internal/nanokvm/auth.go`, `internal/nanokvm/auth_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `func EncryptPassword(password string) (string, error)` — returns URL-encoded base64 of `"Salted__" + salt(8) + AES-256-CBC ciphertext`, using OpenSSL `EVP_BytesToKey` (MD5) key derivation with passphrase `nanokvm-sipeed-2024`.

- [ ] **Step 1: Generate a known-answer vector with OpenSSL**

Run this to understand the format the code must produce (uses a fixed salt so the output is deterministic):

```bash
# password "admin", passphrase "nanokvm-sipeed-2024", fixed salt 0x0102030405060708
printf 'admin' | openssl enc -aes-256-cbc -md md5 -pass pass:nanokvm-sipeed-2024 \
  -S 0102030405060708 -a -A
```

Expected: a base64 string beginning `U2FsdGVkX1` (that is `Salted__` base64-encoded). Record the full output; it is the expected ciphertext for the test.

- [ ] **Step 2: Write the failing test**

Decryption round-trip is the robust check: encrypt with our code, then decrypt with OpenSSL and confirm we recover the plaintext. This avoids hardcoding a salt into production code while still testing against a real, independent implementation.

```go
package nanokvm

import (
	"net/url"
	"os/exec"
	"strings"
	"testing"
)

func TestEncryptPasswordRoundTripsWithOpenSSL(t *testing.T) {
	enc, err := EncryptPassword("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	// EncryptPassword URL-encodes its base64 output; undo that for openssl.
	b64, err := url.QueryUnescape(enc)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("openssl", "enc", "-d", "-aes-256-cbc", "-md", "md5",
		"-pass", "pass:nanokvm-sipeed-2024", "-a", "-A")
	cmd.Stdin = strings.NewReader(b64)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("openssl decrypt failed: %v\n%s", err, out)
	}
	if string(out) != "hunter2" {
		t.Errorf("round trip failed: got %q", out)
	}
}

func TestEncryptPasswordSaltIsRandom(t *testing.T) {
	a, _ := EncryptPassword("x")
	b, _ := EncryptPassword("x")
	if a == b {
		t.Error("ciphertext should differ due to random salt")
	}
}
```

- [ ] **Step 3: Run to verify it fails**

Run: `go test ./internal/nanokvm/ -run TestEncryptPassword`
Expected: FAIL (`EncryptPassword` undefined).

- [ ] **Step 4: Implement `internal/nanokvm/auth.go`**

```go
// Package nanokvm is a client for the NanoKVM firmware HTTP API.
//
// auth.go reimplements the CryptoJS-compatible password encryption used by the
// NanoKVM web frontend. The scheme (OpenSSL "Salted__" format, EVP_BytesToKey
// MD5 key derivation, fixed passphrase) is dictated by the upstream
// GPL-3.0 project github.com/sipeed/NanoKVM; this file is a clean-room Go
// implementation of that wire format and is licensed GPL-3.0 accordingly.
package nanokvm

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"net/url"
)

const nanokvmPassphrase = "nanokvm-sipeed-2024"

// evpBytesToKey derives key+iv the way OpenSSL EVP_BytesToKey (MD5) does,
// matching CryptoJS's default passphrase handling.
func evpBytesToKey(pass, salt []byte, keyLen, ivLen int) (key, iv []byte) {
	var d, prev []byte
	for len(d) < keyLen+ivLen {
		h := md5.New()
		h.Write(prev)
		h.Write(pass)
		h.Write(salt)
		prev = h.Sum(nil)
		d = append(d, prev...)
	}
	return d[:keyLen], d[keyLen : keyLen+ivLen]
}

func pkcs7Pad(b []byte, blockSize int) []byte {
	n := blockSize - len(b)%blockSize
	return append(b, bytes.Repeat([]byte{byte(n)}, n)...)
}

// EncryptPassword returns the URL-encoded base64 of the OpenSSL "Salted__"
// container that the NanoKVM login endpoint expects.
func EncryptPassword(password string) (string, error) {
	salt := make([]byte, 8)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, iv := evpBytesToKey([]byte(nanokvmPassphrase), salt, 32, 16)
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	plain := pkcs7Pad([]byte(password), aes.BlockSize)
	ct := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, plain)

	container := append(append([]byte("Salted__"), salt...), ct...)
	b64 := base64.StdEncoding.EncodeToString(container)
	return url.QueryEscape(b64), nil
}
```

- [ ] **Step 5: Run to verify it passes**

Run: `go test ./internal/nanokvm/ -run TestEncryptPassword -v`
Expected: PASS for both tests.

- [ ] **Step 6: Commit**

```bash
git add internal/nanokvm/auth.go internal/nanokvm/auth_test.go
git commit -m "feat(nanokvm): CryptoJS-compatible password encryption (GPL, ported)"
```

---

### Task 4: Public REST client core with re-auth, plus the fake NanoKVM harness

Fixes the Python fork's permanent-death-on-token-expiry bug: on a `401` or `code == -2` (unauthenticated), the client clears its token and retries the request once.

**Files:**
- Create: `internal/nanokvm/client.go`, `internal/nanokvm/client_test.go`, `internal/nanokvm/fake_test.go`

**Interfaces:**
- Consumes: `EncryptPassword` (Task 3).
- Produces:
  - `type Client struct { ... }`
  - `func New(cfg ClientConfig) *Client` where `type ClientConfig struct { BaseURL string; WSURL string; Username, Password string; HTTPClient *http.Client }`
  - `func (c *Client) Do(ctx context.Context, method, path string, body any) (json.RawMessage, error)` — authenticated request returning the `data` field; re-auths once on auth failure.
  - `func (c *Client) Token(ctx context.Context) (string, error)` — ensures and returns the session token (used by backends for the picoclaw/ws paths).
  - Sentinel: `var ErrAuth = errors.New("nanokvm: authentication failed")`.

- [ ] **Step 1: Write the fake NanoKVM harness `internal/nanokvm/fake_test.go`**

```go
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
	mu          sync.Mutex
	token       string
	loginCalls  int
	expireOnce  bool // if true, the first authed request returns unauthenticated
	handlers    map[string]func() (any, int) // path -> (data, code)
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
```

- [ ] **Step 2: Write the failing client test**

```go
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
```

- [ ] **Step 3: Run to verify it fails**

Run: `go test ./internal/nanokvm/ -run TestDo`
Expected: FAIL (`Client`, `New`, `ClientConfig` undefined).

- [ ] **Step 4: Implement `internal/nanokvm/client.go`**

```go
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
	cfg   ClientConfig
	http  *http.Client
	mu    sync.Mutex
	token string
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
	defer resp.Body.Close()
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
		var d struct{ Token string `json:"token"` }
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
	defer resp.Body.Close()
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
```

- [ ] **Step 5: Run to verify it passes**

Run: `go test ./internal/nanokvm/ -run TestDo -v`
Expected: PASS for both tests.

- [ ] **Step 6: Commit**

```bash
git add internal/nanokvm/client.go internal/nanokvm/client_test.go internal/nanokvm/fake_test.go
git commit -m "feat(nanokvm): REST client with transparent re-auth + fake test harness"
```

---

### Task 5: VM endpoints

**Files:**
- Create: `internal/nanokvm/vm.go`, `internal/nanokvm/vm_test.go`

**Interfaces:**
- Consumes: `(*Client).Do` (Task 4); the fake harness (Task 4).
- Produces methods on `*Client`:
  - `func (c *Client) Power(ctx, action string, durationMs int) error` (action: `"power"`/`"reset"`)
  - `func (c *Client) PowerCycle(ctx, offMs int, sleep func(time.Duration)) error`
  - `func (c *Client) LEDStatus(ctx) (LED, error)` where `type LED struct { PWR bool; HDD bool; HDDAvailable bool }`
  - `func (c *Client) HDMIStatus(ctx) (map[string]any, error)`
  - `func (c *Client) HDMIReset(ctx) error`
  - `func (c *Client) Info(ctx) (map[string]any, error)`
  - `func (c *Client) Hardware(ctx) (Hardware, error)` where `type Hardware struct { Version string; Raw map[string]any }`

- [ ] **Step 1: Write the failing test**

```go
package nanokvm

import (
	"context"
	"testing"
	"time"
)

func TestLEDStatusMarksHDDUnavailableOnBeta(t *testing.T) {
	f := newFakeKVM()
	defer f.Close()
	f.on("/api/vm/gpio", map[string]bool{"pwr": true, "hdd": false})
	f.on("/api/vm/hardware", map[string]any{"version": "beta"})
	c := newTestClient(f)

	hw, err := c.Hardware(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	led, err := c.LEDStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !led.PWR {
		t.Error("pwr should be true")
	}
	// Non-alpha hardware has no HDD LED; report unavailable rather than a fake reading.
	if hw.Version != "beta" {
		t.Fatalf("hw version: %q", hw.Version)
	}
	if led.HDDAvailable {
		t.Error("HDD LED should be reported unavailable on beta hardware")
	}
}

func TestPowerCycleSequences(t *testing.T) {
	f := newFakeKVM()
	defer f.Close()
	f.on("/api/vm/gpio", map[string]any{})
	c := newTestClient(f)

	var slept time.Duration
	if err := c.PowerCycle(context.Background(), 3000, func(d time.Duration) { slept = d }); err != nil {
		t.Fatal(err)
	}
	if slept != 3000*time.Millisecond {
		t.Errorf("power cycle should wait 3000ms, waited %v", slept)
	}
}
```

Note: `LEDStatus` must know the hardware version. It caches the value from `Hardware`. The test calls `Hardware` first to populate that cache; the implementation must also fetch it lazily if not yet known (covered by the implementation below).

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/nanokvm/ -run 'TestLED|TestPowerCycle'`
Expected: FAIL (methods undefined).

- [ ] **Step 3: Implement `internal/nanokvm/vm.go`**

```go
package nanokvm

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

type LED struct {
	PWR          bool `json:"pwr"`
	HDD          bool `json:"hdd"`
	HDDAvailable bool `json:"hdd_available"`
}

type Hardware struct {
	Version string
	Raw     map[string]any
}

var hwOnce sync.Once
var hwVersion string

func (c *Client) Power(ctx context.Context, action string, durationMs int) error {
	_, err := c.Do(ctx, http.MethodPost, "/api/vm/gpio",
		map[string]any{"type": action, "duration": durationMs})
	return err
}

func (c *Client) PowerCycle(ctx context.Context, offMs int, sleep func(time.Duration)) error {
	if sleep == nil {
		sleep = time.Sleep
	}
	if err := c.Power(ctx, "power", 5000); err != nil { // force off
		return err
	}
	sleep(time.Duration(offMs) * time.Millisecond)
	return c.Power(ctx, "power", 800) // power on
}

func (c *Client) Hardware(ctx context.Context) (Hardware, error) {
	raw, err := c.Do(ctx, http.MethodGet, "/api/vm/hardware", nil)
	if err != nil {
		return Hardware{}, err
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	v, _ := m["version"].(string)
	hwOnce.Do(func() { hwVersion = strings.ToLower(v) })
	return Hardware{Version: strings.ToLower(v), Raw: m}, nil
}

func (c *Client) LEDStatus(ctx context.Context) (LED, error) {
	raw, err := c.Do(ctx, http.MethodGet, "/api/vm/gpio", nil)
	if err != nil {
		return LED{}, err
	}
	var led LED
	_ = json.Unmarshal(raw, &led)
	// HDD LED exists only on alpha hardware (upstream gpio.go hardcodes hdd=false
	// otherwise). Report availability so the tool does not present a fake reading.
	if hwVersion == "" {
		if hw, err := c.Hardware(ctx); err == nil {
			hwVersion = hw.Version
		}
	}
	led.HDDAvailable = hwVersion == "alpha"
	return led, nil
}

func (c *Client) HDMIStatus(ctx context.Context) (map[string]any, error) {
	raw, err := c.Do(ctx, http.MethodGet, "/api/vm/hdmi", nil)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m, nil
}

func (c *Client) HDMIReset(ctx context.Context) error {
	_, err := c.Do(ctx, http.MethodPost, "/api/vm/hdmi/reset", nil)
	return err
}

func (c *Client) Info(ctx context.Context) (map[string]any, error) {
	raw, err := c.Do(ctx, http.MethodGet, "/api/vm/info", nil)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m, nil
}
```

Note on `hwOnce`/`hwVersion` package globals: acceptable because one process serves one device. If multi-device is ever added, move these onto `Client`. Flagged so a reviewer does not treat it as an oversight.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/nanokvm/ -run 'TestLED|TestPowerCycle' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/nanokvm/vm.go internal/nanokvm/vm_test.go
git commit -m "feat(nanokvm): power, power-cycle, LED (beta-aware), HDMI, info, hardware"
```

---

### Task 6: Storage endpoints

**Files:**
- Create: `internal/nanokvm/storage.go`, `internal/nanokvm/storage_test.go`

**Interfaces:**
- Consumes: `(*Client).Do`; fake harness.
- Produces methods on `*Client`:
  - `func (c *Client) ListImages(ctx) ([]any, error)`
  - `func (c *Client) MountImage(ctx, file string, cdrom bool) error`
  - `func (c *Client) UnmountImage(ctx) error`
  - `func (c *Client) MountedImage(ctx) (map[string]any, error)`

- [ ] **Step 1: Write the failing test**

```go
package nanokvm

import (
	"context"
	"testing"
)

func TestStorageRoundtrip(t *testing.T) {
	f := newFakeKVM()
	defer f.Close()
	f.on("/api/storage/image", []any{"/data/ubuntu.iso"})
	f.on("/api/storage/image/mount", map[string]any{})
	f.on("/api/storage/image/mounted", map[string]any{"file": "/data/ubuntu.iso"})
	c := newTestClient(f)

	imgs, err := c.ListImages(context.Background())
	if err != nil || len(imgs) != 1 {
		t.Fatalf("list: %v %v", imgs, err)
	}
	if err := c.MountImage(context.Background(), "/data/ubuntu.iso", true); err != nil {
		t.Fatal(err)
	}
	m, err := c.MountedImage(context.Background())
	if err != nil || m["file"] != "/data/ubuntu.iso" {
		t.Fatalf("mounted: %v %v", m, err)
	}
	if err := c.UnmountImage(context.Background()); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/nanokvm/ -run TestStorage`
Expected: FAIL (methods undefined).

- [ ] **Step 3: Implement `internal/nanokvm/storage.go`**

```go
package nanokvm

import (
	"context"
	"encoding/json"
	"net/http"
)

func (c *Client) ListImages(ctx context.Context) ([]any, error) {
	raw, err := c.Do(ctx, http.MethodGet, "/api/storage/image", nil)
	if err != nil {
		return nil, err
	}
	var out []any
	_ = json.Unmarshal(raw, &out)
	return out, nil
}

func (c *Client) MountImage(ctx context.Context, file string, cdrom bool) error {
	_, err := c.Do(ctx, http.MethodPost, "/api/storage/image/mount",
		map[string]any{"file": file, "cdrom": cdrom})
	return err
}

func (c *Client) UnmountImage(ctx context.Context) error {
	_, err := c.Do(ctx, http.MethodPost, "/api/storage/image/mount", map[string]any{})
	return err
}

func (c *Client) MountedImage(ctx context.Context) (map[string]any, error) {
	raw, err := c.Do(ctx, http.MethodGet, "/api/storage/image/mounted", nil)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m, nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/nanokvm/ -run TestStorage -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/nanokvm/storage.go internal/nanokvm/storage_test.go
git commit -m "feat(nanokvm): ISO storage list/mount/unmount/mounted"
```

---

## Phase 2 — Backends

### Task 7: Backend interface and shared types

**Files:**
- Create: `internal/backend/backend.go`, `internal/backend/backend_test.go`

**Interfaces:**
- Consumes: nothing (pure types).
- Produces:
  - `type Action struct { Action string; X, Y *float64; From, To *Point; Button, Text string; Keys []string; Direction string; Amount, DurationMs int }`
  - `type Point struct { X, Y *float64 }`
  - `type ScreenshotOpts struct { Width, Height, Quality int }`
  - `type Shot struct { JPEG []byte; Width, Height int }`
  - `type KVMBackend interface { Name() string; Screenshot(ctx, ScreenshotOpts) (Shot, error); Input(ctx, []Action) error }`
  - `func ValidateActions(actions []Action) error` — rejects unknown action verbs and out-of-range normalized coords.

- [ ] **Step 1: Write the failing test**

```go
package backend

import "testing"

func f64(v float64) *float64 { return &v }

func TestValidateActions(t *testing.T) {
	if err := ValidateActions([]Action{{Action: "click", X: f64(0.5), Y: f64(0.5)}}); err != nil {
		t.Errorf("valid click rejected: %v", err)
	}
	if err := ValidateActions([]Action{{Action: "warp"}}); err == nil {
		t.Error("unknown verb should be rejected")
	}
	if err := ValidateActions([]Action{{Action: "move", X: f64(1.5), Y: f64(0.5)}}); err == nil {
		t.Error("out-of-range coord should be rejected")
	}
	if err := ValidateActions([]Action{{Action: "type", Text: "hi"}}); err != nil {
		t.Errorf("valid type rejected: %v", err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/backend/`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement `internal/backend/backend.go`**

```go
// Package backend abstracts screen capture and HID input, which have both a
// preferred on-device implementation (picoclaw) and a public-API fallback.
package backend

import (
	"context"
	"fmt"
)

type Point struct {
	X *float64 `json:"x"`
	Y *float64 `json:"y"`
}

type Action struct {
	Action     string   `json:"action"`
	X          *float64 `json:"x,omitempty"`
	Y          *float64 `json:"y,omitempty"`
	From       *Point   `json:"from,omitempty"`
	To         *Point   `json:"to,omitempty"`
	Button     string   `json:"button,omitempty"`
	Text       string   `json:"text,omitempty"`
	Keys       []string `json:"keys,omitempty"`
	Direction  string   `json:"direction,omitempty"`
	Amount     int      `json:"amount,omitempty"`
	DurationMs int      `json:"duration_ms,omitempty"`
}

type ScreenshotOpts struct {
	Width   int
	Height  int
	Quality int
}

type Shot struct {
	JPEG   []byte
	Width  int
	Height int
}

type KVMBackend interface {
	Name() string
	Screenshot(ctx context.Context, opts ScreenshotOpts) (Shot, error)
	Input(ctx context.Context, actions []Action) error
}

var validVerbs = map[string]bool{
	"click": true, "move": true, "type": true, "hotkey": true,
	"scroll": true, "drag": true, "wait": true,
}

func inRange(p *float64) bool { return p == nil || (*p >= 0 && *p <= 1) }

// ValidateActions rejects unknown verbs and normalized coordinates outside [0,1].
func ValidateActions(actions []Action) error {
	if len(actions) == 0 {
		return fmt.Errorf("no actions supplied")
	}
	for i, a := range actions {
		if !validVerbs[a.Action] {
			return fmt.Errorf("action %d: unknown verb %q", i, a.Action)
		}
		if !inRange(a.X) || !inRange(a.Y) {
			return fmt.Errorf("action %d: coordinates must be normalized to [0,1]", i)
		}
		if a.From != nil && (!inRange(a.From.X) || !inRange(a.From.Y)) {
			return fmt.Errorf("action %d: from coordinates must be normalized to [0,1]", i)
		}
		if a.To != nil && (!inRange(a.To.X) || !inRange(a.To.Y)) {
			return fmt.Errorf("action %d: to coordinates must be normalized to [0,1]", i)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/backend/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/backend.go internal/backend/backend_test.go
git commit -m "feat(backend): KVMBackend interface, action types, validation"
```

---

### Task 8: picoclawBackend

Uses the firmware's internal loopback API. Requests raw JPEG bytes (omitting `format=base64`) so no decode happens on our side. Sends the internal token and an arbitrary session ID.

**Files:**
- Create: `internal/backend/picoclaw.go`, `internal/backend/picoclaw_test.go`

**Interfaces:**
- Consumes: `Action`, `Shot`, `ScreenshotOpts`, `ValidateActions` (Task 7).
- Produces:
  - `func NewPicoclaw(baseURL, token, sessionID string, hc *http.Client) *Picoclaw`
  - `Picoclaw` implements `KVMBackend`.
  - `func ReadInternalToken(path string) (string, error)` — trims the token file.
  - Const `InternalTokenHeader = "X-NanoKVM-Internal-Token"`, `SessionIDHeader = "X-PicoClaw-Session-ID"`.

- [ ] **Step 1: Write the failing test with a fake picoclaw server**

```go
package backend

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/backend/ -run TestPicoclaw`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement `internal/backend/picoclaw.go`**

```go
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
	b, err := os.ReadFile(path)
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
	defer resp.Body.Close()
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
	defer resp.Body.Close()
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
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/backend/ -run TestPicoclaw -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/picoclaw.go internal/backend/picoclaw_test.go
git commit -m "feat(backend): picoclaw backend (raw-jpeg passthrough, batched actions)"
```

---

### Task 9: Backend selection

Selects `picoclaw` if the token file is present and a probe succeeds; otherwise `public`. Selection happens once at startup and is logged.

**Files:**
- Create: `internal/backend/select.go`, `internal/backend/select_test.go`

**Interfaces:**
- Consumes: `KVMBackend`, `Picoclaw` (Tasks 7–8); `Public` (Task 15 — for the wiring path; selection is written to accept an injected fallback so it does not hard-depend on Task 15's internals).
- Produces:
  - `type Deps struct { BaseURL string; TokenPath string; SessionID string; HTTP *http.Client; Fallback KVMBackend; Probe bool }`
  - `func Select(ctx context.Context, d Deps) (KVMBackend, error)` — returns picoclaw when viable, else `d.Fallback`.

- [ ] **Step 1: Write the failing test**

```go
package backend

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

type stubBackend struct{ name string }

func (s stubBackend) Name() string                                          { return s.name }
func (s stubBackend) Screenshot(context.Context, ScreenshotOpts) (Shot, error) { return Shot{}, nil }
func (s stubBackend) Input(context.Context, []Action) error                 { return nil }

func TestSelectPrefersPicoclawWhenProbeSucceeds(t *testing.T) {
	srv, _ := fakePicoclaw(t, "tok")
	dir := t.TempDir()
	tokFile := filepath.Join(dir, "tok")
	os.WriteFile(tokFile, []byte("tok\n"), 0o600)

	b, err := Select(context.Background(), Deps{
		BaseURL: srv.URL, TokenPath: tokFile, SessionID: "s", HTTP: srv.Client(),
		Fallback: stubBackend{"public"}, Probe: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if b.Name() != "picoclaw" {
		t.Errorf("want picoclaw, got %s", b.Name())
	}
}

func TestSelectFallsBackWhenNoToken(t *testing.T) {
	b, err := Select(context.Background(), Deps{
		BaseURL: "http://127.0.0.1:0", TokenPath: "/nonexistent",
		Fallback: stubBackend{"public"}, Probe: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if b.Name() != "public" {
		t.Errorf("want public fallback, got %s", b.Name())
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/backend/ -run TestSelect`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement `internal/backend/select.go`**

```go
package backend

import (
	"context"
	"log"
	"net/http"
)

type Deps struct {
	BaseURL   string
	TokenPath string
	SessionID string
	HTTP      *http.Client
	Fallback  KVMBackend
	Probe     bool
}

// Select returns the picoclaw backend when its token file is present and a probe
// screenshot succeeds; otherwise the provided fallback. The decision is logged.
func Select(ctx context.Context, d Deps) (KVMBackend, error) {
	token, err := ReadInternalToken(d.TokenPath)
	if err != nil {
		log.Printf("backend: picoclaw token unavailable (%v); using %s", err, d.Fallback.Name())
		return d.Fallback, nil
	}
	p := NewPicoclaw(d.BaseURL, token, d.SessionID, d.HTTP)
	if d.Probe {
		if _, err := p.Screenshot(ctx, ScreenshotOpts{Width: 64}); err != nil {
			log.Printf("backend: picoclaw probe failed (%v); using %s", err, d.Fallback.Name())
			return d.Fallback, nil
		}
	}
	log.Printf("backend: using picoclaw")
	return p, nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/backend/ -run TestSelect -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/select.go internal/backend/select_test.go
git commit -m "feat(backend): startup backend selection with probe and fallback"
```

---

## Phase 3 — MCP server (produces a working daemon)

### Task 10: Audit log with text redaction

**Files:**
- Create: `internal/audit/audit.go`, `internal/audit/audit_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Logger struct { ... }`
  - `func New(w io.Writer, full bool) *Logger`
  - `func (l *Logger) Record(tool, backend string, args map[string]any, err error)` — writes one JSON line; when `full` is false, any `text` argument (and `text` inside `actions`) is replaced with `"len=<n> sha=<8hex>"`.

- [ ] **Step 1: Write the failing test**

```go
package audit

import (
	"bytes"
	"strings"
	"testing"
)

func TestRedactsTextByDefault(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, false)
	l.Record("nanokvm_input", "picoclaw", map[string]any{
		"actions": []map[string]any{{"action": "type", "text": "s3cret"}},
	}, nil)
	out := buf.String()
	if strings.Contains(out, "s3cret") {
		t.Error("secret text must not appear in audit log by default")
	}
	if !strings.Contains(out, "len=6") {
		t.Errorf("expected redaction marker, got %s", out)
	}
}

func TestFullModeKeepsText(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, true)
	l.Record("nanokvm_input", "picoclaw", map[string]any{"text": "visible"}, nil)
	if !strings.Contains(buf.String(), "visible") {
		t.Error("full mode should keep text")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/audit/`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement `internal/audit/audit.go`**

```go
// Package audit records mutating tool calls, redacting typed text by default.
package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

type Logger struct {
	mu   sync.Mutex
	w    io.Writer
	full bool
	now  func() time.Time
}

func New(w io.Writer, full bool) *Logger {
	return &Logger{w: w, full: full, now: time.Now}
}

func redactString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("len=%d sha=%s", len(s), hex.EncodeToString(sum[:])[:8])
}

// redact walks the args recursively, replacing any "text" value unless full mode.
func (l *Logger) redact(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if k == "text" {
				if s, ok := val.(string); ok {
					out[k] = redactString(s)
					continue
				}
			}
			out[k] = l.redact(val)
		}
		return out
	case []map[string]any:
		arr := make([]any, len(t))
		for i, e := range t {
			arr[i] = l.redact(e)
		}
		return arr
	case []any:
		arr := make([]any, len(t))
		for i, e := range t {
			arr[i] = l.redact(e)
		}
		return arr
	default:
		return v
	}
}

func (l *Logger) Record(tool, backend string, args map[string]any, err error) {
	a := any(args)
	if !l.full {
		a = l.redact(args)
	}
	entry := map[string]any{
		"ts":      l.now().UTC().Format(time.RFC3339),
		"tool":    tool,
		"backend": backend,
		"args":    a,
		"ok":      err == nil,
	}
	if err != nil {
		entry["error"] = err.Error()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = json.NewEncoder(l.w).Encode(entry)
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/audit/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/audit/
git commit -m "feat(audit): mutating-call audit log with default text redaction"
```

---

### Task 11: Bearer-token middleware

**Files:**
- Create: `internal/httpauth/bearer.go`, `internal/httpauth/bearer_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `func Bearer(token string, next http.Handler) http.Handler` — 401 unless `Authorization: Bearer <token>` matches (constant-time).

- [ ] **Step 1: Write the failing test**

```go
package httpauth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBearer(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := Bearer("secret", ok)

	cases := []struct {
		hdr  string
		want int
	}{
		{"Bearer secret", 200},
		{"Bearer wrong", 401},
		{"", 401},
		{"secret", 401},
	}
	for _, c := range cases {
		r := httptest.NewRequest("POST", "/", nil)
		if c.hdr != "" {
			r.Header.Set("Authorization", c.hdr)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("hdr %q: want %d got %d", c.hdr, c.want, w.Code)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/httpauth/`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement `internal/httpauth/bearer.go`**

```go
// Package httpauth provides bearer-token authentication middleware.
package httpauth

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

func Bearer(token string, next http.Handler) http.Handler {
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		if !strings.HasPrefix(got, "Bearer ") || subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/httpauth/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/httpauth/
git commit -m "feat(httpauth): constant-time bearer token middleware"
```

---

### Task 12: MCP server, tools, and read-only filter

Registers all 14 tools. Mutating tools are registered only when not in read-only mode, so they never appear in `tools/list`. Each mutating handler records to the audit log.

**Files:**
- Create: `internal/mcpserver/server.go`, `internal/mcpserver/tools.go`, `internal/mcpserver/server_test.go`

**Interfaces:**
- Consumes: `*nanokvm.Client` (Tasks 4–6), `backend.KVMBackend` + `backend.Action` (Tasks 7–9), `*audit.Logger` (Task 10).
- Produces:
  - `type Deps struct { KVM *nanokvm.Client; Backend backend.KVMBackend; Audit *audit.Logger; ReadOnly bool }`
  - `func New(d Deps) *mcp.Server` — a configured MCP server with tools registered.
  - Tool names exactly: `nanokvm_screenshot`, `nanokvm_led_status`, `nanokvm_hdmi_status`, `nanokvm_list_images`, `nanokvm_mounted_image`, `nanokvm_info`, `nanokvm_hardware` (read-only); `nanokvm_input`, `nanokvm_power`, `nanokvm_power_cycle`, `nanokvm_mount_iso`, `nanokvm_unmount_iso`, `nanokvm_hdmi_reset`, `nanokvm_reset_hid` (mutating).

- [ ] **Step 1: Write the failing test (tool registration + read-only filter)**

```go
package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/scgreenhalgh/nanokvm-mcp/internal/backend"
)

type nopBackend struct{}

func (nopBackend) Name() string { return "nop" }
func (nopBackend) Screenshot(context.Context, backend.ScreenshotOpts) (backend.Shot, error) {
	return backend.Shot{JPEG: []byte{0xFF, 0xD8, 0xFF, 0xD9}}, nil
}
func (nopBackend) Input(context.Context, []backend.Action) error { return nil }

func listToolNames(t *testing.T, s *mcp.Server) map[string]bool {
	// Use an in-memory client/server session to enumerate tools.
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := s.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tl := range res.Tools {
		names[tl.Name] = true
	}
	return names
}

func TestReadOnlyModeHidesMutatingTools(t *testing.T) {
	s := New(Deps{Backend: nopBackend{}, ReadOnly: true})
	names := listToolNames(t, s)
	if !names["nanokvm_screenshot"] {
		t.Error("read-only tool should be present")
	}
	if names["nanokvm_power"] || names["nanokvm_input"] {
		t.Error("mutating tools must be absent in read-only mode")
	}
}

func TestFullModeExposesAllTools(t *testing.T) {
	s := New(Deps{Backend: nopBackend{}, ReadOnly: false})
	names := listToolNames(t, s)
	for _, want := range []string{"nanokvm_screenshot", "nanokvm_power", "nanokvm_input", "nanokvm_reset_hid"} {
		if !names[want] {
			t.Errorf("expected tool %s", want)
		}
	}
}
```

Note: confirm `mcp.NewInMemoryTransports` and `(*ClientSession).ListTools` names against the installed SDK (`go doc github.com/modelcontextprotocol/go-sdk/mcp NewInMemoryTransports`); adjust the enumeration helper if the SDK spells them differently. The registration behavior under test does not change.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/mcpserver/`
Expected: FAIL (undefined `New`/`Deps`).

- [ ] **Step 3: Implement `internal/mcpserver/server.go`**

```go
// Package mcpserver builds the MCP server and registers NanoKVM tools.
package mcpserver

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/scgreenhalgh/nanokvm-mcp/internal/audit"
	"github.com/scgreenhalgh/nanokvm-mcp/internal/backend"
	"github.com/scgreenhalgh/nanokvm-mcp/internal/nanokvm"
)

type Deps struct {
	KVM      *nanokvm.Client
	Backend  backend.KVMBackend
	Audit    *audit.Logger
	ReadOnly bool
}

func ptr[T any](v T) *T { return &v }

func New(d Deps) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "nanokvm", Version: "0.1.0"}, nil)
	registerReadOnly(s, d)
	if !d.ReadOnly {
		registerMutating(s, d)
	}
	return s
}
```

- [ ] **Step 4: Implement `internal/mcpserver/tools.go`**

```go
package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/scgreenhalgh/nanokvm-mcp/internal/backend"
)

// ---- read-only tools ----

type emptyArgs struct{}

type ledOut struct {
	PWR          bool `json:"pwr"`
	HDD          bool `json:"hdd"`
	HDDAvailable bool `json:"hdd_available" jsonschema:"whether this hardware has an HDD LED"`
}

type screenshotArgs struct {
	Width   int `json:"width,omitempty" jsonschema:"max width in px; 0 for backend default"`
	Height  int `json:"height,omitempty" jsonschema:"max height in px; 0 for backend default"`
	Quality int `json:"quality,omitempty" jsonschema:"JPEG quality 1-100; 0 for backend default"`
}

func registerReadOnly(s *mcp.Server, d Deps) {
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(false)}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "nanokvm_screenshot",
		Description: "Capture the target machine's screen as a JPEG image.",
		Annotations: ro,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in screenshotArgs) (*mcp.CallToolResult, any, error) {
		shot, err := d.Backend.Screenshot(ctx, backend.ScreenshotOpts{Width: in.Width, Height: in.Height, Quality: in.Quality})
		if err != nil {
			return nil, nil, err
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.ImageContent{Data: shot.JPEG, MIMEType: "image/jpeg"}},
		}, nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "nanokvm_led_status", Description: "Read the power and HDD LEDs.", Annotations: ro,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, ledOut, error) {
		led, err := d.KVM.LEDStatus(ctx)
		if err != nil {
			return nil, ledOut{}, err
		}
		return nil, ledOut{PWR: led.PWR, HDD: led.HDD, HDDAvailable: led.HDDAvailable}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "nanokvm_hdmi_status", Description: "Get HDMI signal state and resolution.", Annotations: ro,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, map[string]any, error) {
		m, err := d.KVM.HDMIStatus(ctx)
		return nil, m, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "nanokvm_list_images", Description: "List available ISO images on the device.", Annotations: ro,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, map[string]any, error) {
		imgs, err := d.KVM.ListImages(ctx)
		return nil, map[string]any{"images": imgs}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "nanokvm_mounted_image", Description: "Get the currently mounted ISO, if any.", Annotations: ro,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, map[string]any, error) {
		m, err := d.KVM.MountedImage(ctx)
		return nil, m, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "nanokvm_info", Description: "Get NanoKVM device information.", Annotations: ro,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, map[string]any, error) {
		m, err := d.KVM.Info(ctx)
		return nil, m, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "nanokvm_hardware", Description: "Get NanoKVM hardware details.", Annotations: ro,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, map[string]any, error) {
		hw, err := d.KVM.Hardware(ctx)
		if err != nil {
			return nil, nil, err
		}
		return nil, hw.Raw, nil
	})
}

// ---- mutating tools ----

type inputArgs struct {
	Actions []backend.Action `json:"actions" jsonschema:"ordered HID actions; mouse coords normalized to [0,1]"`
}

type powerArgs struct {
	Action string `json:"action" jsonschema:"one of: power, power_long, reset"`
}

type powerCycleArgs struct {
	OffMs int `json:"off_ms,omitempty" jsonschema:"ms to wait between off and on; default 3000"`
}

type mountArgs struct {
	File   string `json:"file" jsonschema:"ISO path on the NanoKVM device"`
	CDROM  bool   `json:"cdrom,omitempty" jsonschema:"mount as CD-ROM (default) vs USB disk"`
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func registerMutating(s *mcp.Server, d Deps) {
	destructive := &mcp.ToolAnnotations{DestructiveHint: ptr(true), OpenWorldHint: ptr(false)}
	idempotent := &mcp.ToolAnnotations{DestructiveHint: ptr(false), IdempotentHint: true, OpenWorldHint: ptr(false)}

	rec := func(tool string, args map[string]any, err error) {
		if d.Audit != nil {
			d.Audit.Record(tool, d.Backend.Name(), args, err)
		}
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "nanokvm_input",
		Description: "Send a batch of HID actions (click, move, type, hotkey, scroll, drag, wait). Mouse coordinates are normalized to [0,1] from the top-left.",
		Annotations: destructive,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in inputArgs) (*mcp.CallToolResult, any, error) {
		err := d.Backend.Input(ctx, in.Actions)
		rec("nanokvm_input", map[string]any{"actions": in.Actions}, err)
		if err != nil {
			return nil, nil, err
		}
		return text(fmt.Sprintf("executed %d actions", len(in.Actions))), nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "nanokvm_power",
		Description: "Press the power or reset button. action=power (short), power_long (force off), reset (no-op on boards without a reset line).",
		Annotations: destructive,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in powerArgs) (*mcp.CallToolResult, any, error) {
		var err error
		switch in.Action {
		case "power":
			err = d.KVM.Power(ctx, "power", 800)
		case "power_long":
			err = d.KVM.Power(ctx, "power", 5000)
		case "reset":
			err = d.KVM.Power(ctx, "reset", 800)
		default:
			err = fmt.Errorf("invalid action %q", in.Action)
		}
		rec("nanokvm_power", map[string]any{"action": in.Action}, err)
		if err != nil {
			return nil, nil, err
		}
		return text("power action sent: " + in.Action), nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "nanokvm_power_cycle", Description: "Force off, wait, then power on. Recommended reset for boards without a reset line.", Annotations: destructive,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in powerCycleArgs) (*mcp.CallToolResult, any, error) {
		off := in.OffMs
		if off == 0 {
			off = 3000
		}
		err := d.KVM.PowerCycle(ctx, off, nil)
		rec("nanokvm_power_cycle", map[string]any{"off_ms": off}, err)
		if err != nil {
			return nil, nil, err
		}
		return text(fmt.Sprintf("power cycled (waited %dms)", off)), nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "nanokvm_mount_iso", Description: "Mount an ISO image to the target as CD-ROM or USB disk.", Annotations: destructive,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in mountArgs) (*mcp.CallToolResult, any, error) {
		err := d.KVM.MountImage(ctx, in.File, in.CDROM)
		rec("nanokvm_mount_iso", map[string]any{"file": in.File, "cdrom": in.CDROM}, err)
		if err != nil {
			return nil, nil, err
		}
		return text("mounted " + in.File), nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "nanokvm_unmount_iso", Description: "Unmount the currently mounted ISO.", Annotations: destructive,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
		err := d.KVM.UnmountImage(ctx)
		rec("nanokvm_unmount_iso", map[string]any{}, err)
		if err != nil {
			return nil, nil, err
		}
		return text("unmounted"), nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "nanokvm_hdmi_reset", Description: "Reset the HDMI capture pipeline (affects capture, not the target).", Annotations: idempotent,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
		err := d.KVM.HDMIReset(ctx)
		rec("nanokvm_hdmi_reset", map[string]any{}, err)
		if err != nil {
			return nil, nil, err
		}
		return text("hdmi reset"), nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "nanokvm_reset_hid", Description: "Reset the USB HID gadget if keyboard/mouse input stops working.", Annotations: idempotent,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
		_, err := d.KVM.Do(ctx, "POST", "/api/hid/reset", nil)
		rec("nanokvm_reset_hid", map[string]any{}, err)
		if err != nil {
			return nil, nil, err
		}
		return text("hid reset"), nil, nil
	})
}
```

- [ ] **Step 5: Run to verify it passes**

Run: `go test ./internal/mcpserver/ -v`
Expected: PASS (both registration tests).

- [ ] **Step 6: Commit**

```bash
git add internal/mcpserver/
git commit -m "feat(mcpserver): 14 annotated tools with read-only filter and audit hooks"
```

---

### Task 13: Wire everything in main and serve (working daemon)

**Files:**
- Modify: `cmd/nanokvm-mcp/main.go`
- Create: `cmd/nanokvm-mcp/main_test.go`

**Interfaces:**
- Consumes: all prior packages.
- Produces: a runnable server. `func run() error` is testable; `main` calls it.

- [ ] **Step 1: Write a smoke test that the server starts and rejects unauthed requests**

```go
package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestServerRejectsUnauthed(t *testing.T) {
	t.Setenv("NANOKVM_HOST", "127.0.0.1")
	t.Setenv("NANOKVM_MCP_TOKEN", "smoke-token")
	t.Setenv("NANOKVM_MCP_BIND", "127.0.0.1:8199")
	t.Setenv("NANOKVM_MCP_AUDIT", t.TempDir()+"/audit.log")
	t.Setenv("NANOKVM_MCP_READONLY", "true") // avoid needing a backend probe

	go func() { _ = run() }()
	time.Sleep(200 * time.Millisecond)

	// No Authorization header -> 401.
	resp, err := http.Post("http://127.0.0.1:8199/", "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("want 401 without bearer, got %d", resp.StatusCode)
	}
}
```

Note: `run()` must return promptly after `ListenAndServe` is called in a goroutine, or the test must call it in a goroutine (as above). Ensure `run()` blocks on serving so the goroutine keeps the listener open; the test never returns from `run()`, which is fine.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/nanokvm-mcp/`
Expected: FAIL (`run` undefined).

- [ ] **Step 3: Implement `cmd/nanokvm-mcp/main.go`**

```go
// Command nanokvm-mcp is an MCP server for Sipeed NanoKVM devices.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/scgreenhalgh/nanokvm-mcp/internal/audit"
	"github.com/scgreenhalgh/nanokvm-mcp/internal/backend"
	"github.com/scgreenhalgh/nanokvm-mcp/internal/config"
	"github.com/scgreenhalgh/nanokvm-mcp/internal/httpauth"
	"github.com/scgreenhalgh/nanokvm-mcp/internal/mcpserver"
	"github.com/scgreenhalgh/nanokvm-mcp/internal/nanokvm"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	scheme := "http"
	wsScheme := "ws"
	if cfg.UseHTTPS {
		scheme, wsScheme = "https", "wss"
	}
	baseURL := scheme + "://" + cfg.Host
	kvm := nanokvm.New(nanokvm.ClientConfig{
		BaseURL:  baseURL,
		WSURL:    wsScheme + "://" + cfg.Host + "/api/ws",
		Username: cfg.Username,
		Password: cfg.Password,
	})

	// Audit log.
	if err := os.MkdirAll(filepath.Dir(cfg.AuditPath), 0o755); err != nil {
		log.Printf("audit dir: %v (logging to stderr)", err)
	}
	var auditW = os.Stderr
	if f, err := os.OpenFile(cfg.AuditPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		auditW = f
	} else {
		log.Printf("audit file: %v (logging to stderr)", err)
	}
	aud := audit.New(auditW, cfg.AuditFull)

	// Backend selection.
	pub := backend.NewPublic(kvm) // Task 15
	be, err := backend.Select(context.Background(), backend.Deps{
		BaseURL:   baseURL,
		TokenPath: backend.DefaultTokenPath,
		SessionID: "nanokvm-mcp",
		Fallback:  pub,
		Probe:     true,
	})
	if err != nil {
		return err
	}

	srv := mcpserver.New(mcpserver.Deps{KVM: kvm, Backend: be, Audit: aud, ReadOnly: cfg.ReadOnly})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	authed := httpauth.Bearer(cfg.BearerToken, handler)

	log.Printf("nanokvm-mcp %s: backend=%s readonly=%v listening on %s", version, be.Name(), cfg.ReadOnly, cfg.BindAddr)
	if os.Getenv("NANOKVM_MCP_TOKEN") == "" {
		log.Printf("generated bearer token: %s", cfg.BearerToken)
	}
	return http.ListenAndServe(cfg.BindAddr, authed)
}
```

Note: this references `backend.NewPublic` from Task 15. To keep Task 13 independently green, first add a minimal stub in `internal/backend/public.go` returning a "public backend not yet implemented" error from `Screenshot`/`Input` but a valid `Name() == "public"`; Task 15 replaces the stub with the real implementation. Create that stub as Step 3a below.

- [ ] **Step 3a: Add the minimal public backend stub so main compiles**

```go
// internal/backend/public.go
package backend

import (
	"context"
	"errors"

	"github.com/scgreenhalgh/nanokvm-mcp/internal/nanokvm"
)

type Public struct{ kvm *nanokvm.Client }

func NewPublic(kvm *nanokvm.Client) *Public { return &Public{kvm: kvm} }
func (p *Public) Name() string              { return "public" }
func (p *Public) Screenshot(context.Context, ScreenshotOpts) (Shot, error) {
	return Shot{}, errors.New("public backend screenshot not implemented")
}
func (p *Public) Input(context.Context, []Action) error {
	return errors.New("public backend input not implemented")
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./cmd/nanokvm-mcp/ -v && mise run build`
Expected: smoke test PASS; riscv64 build succeeds.

- [ ] **Step 5: Commit**

```bash
git add cmd/nanokvm-mcp/ internal/backend/public.go
git commit -m "feat: wire config, backend, audit, MCP server, bearer auth into a running daemon"
```

---

## Phase 4 — Resilience and operations

### Task 14: Upstream route drift detector

Fetches upstream router source at a pinned ref and asserts every public path the sidecar depends on still exists. This is the guard against the `/api/vm/gpio/led` class of bug.

**Files:**
- Create: `tools/apicheck/apicheck_test.go`, `tools/apicheck/routes.txt`

**Interfaces:**
- Consumes: network access to `raw.githubusercontent.com` (skips with a clear message when offline).
- Produces: a `go test` that fails when a required route is absent upstream.

- [ ] **Step 1: Write `tools/apicheck/routes.txt` (paths we depend on)**

```
/api/auth/login
/api/vm/gpio
/api/vm/hdmi
/api/vm/hdmi/reset
/api/vm/info
/api/vm/hardware
/api/storage/image
/api/storage/image/mounted
/api/storage/image/mount
/api/hid/paste
/api/hid/reset
/api/stream/mjpeg
/api/picoclaw/screenshot
/api/picoclaw/actions
```

- [ ] **Step 2: Write the failing test**

```go
package apicheck

import (
	"bufio"
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// Pinned upstream ref; bump deliberately when re-vendoring route knowledge.
const ref = "main"

var routerFiles = []string{
	"server/router/auth.go", "server/router/vm.go", "server/router/hid.go",
	"server/router/storage.go", "server/router/stream.go", "server/router/picoclaw.go",
}

func fetch(t *testing.T, path string) string {
	url := "https://raw.githubusercontent.com/sipeed/NanoKVM/" + ref + "/" + path
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Skipf("offline or upstream unreachable: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("fetch %s: HTTP %d", path, resp.StatusCode)
	}
	buf := new(strings.Builder)
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		buf.WriteString(sc.Text())
		buf.WriteByte('\n')
	}
	return buf.String()
}

func TestRequiredRoutesExistUpstream(t *testing.T) {
	if os.Getenv("APICHECK_OFFLINE") == "1" {
		t.Skip("APICHECK_OFFLINE set")
	}
	corpus := ""
	for _, f := range routerFiles {
		corpus += fetch(t, f)
	}
	f, err := os.Open("routes.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		route := strings.TrimSpace(sc.Text())
		if route == "" {
			continue
		}
		// Router paths are registered without the /api prefix inside groups,
		// so match the suffix after /api.
		needle := strings.TrimPrefix(route, "/api")
		if !strings.Contains(corpus, "\""+needle+"\"") {
			isPicoclaw := strings.HasPrefix(route, "/api/picoclaw")
			if isPicoclaw {
				t.Errorf("INTERNAL route missing upstream (unstable API): %s", route)
			} else {
				t.Errorf("PUBLIC route we depend on is missing upstream: %s", route)
			}
		}
	}
}
```

Note: picoclaw paths are registered as path constants (e.g. `picoclawScreenshotPath = "/screenshot"`) rather than string literals of the full path, so the suffix match may need adjusting to the constant values (`"/screenshot"`, `"/actions"`). Verify against the fetched `picoclaw.go` and adjust `needle` for those two entries; the intent (fail on drift) is unchanged.

- [ ] **Step 3: Run to verify it passes (or skips offline)**

Run: `go test ./tools/apicheck/ -v`
Expected: PASS online (all routes found), or SKIP offline. `/api/vm/gpio/led` is intentionally absent from `routes.txt`.

- [ ] **Step 4: Re-enable the apicheck CI step**

If it was commented out in Task 1, restore the `mise run apicheck` line in `.github/workflows/ci.yml`.

- [ ] **Step 5: Commit**

```bash
git add tools/apicheck/ .github/workflows/ci.yml
git commit -m "feat(apicheck): fail CI when an upstream route we depend on disappears"
```

---

### Task 15: Public backend screenshot (fallback, with hard resolution cap)

Replaces the stub `Screenshot`. Pulls one JPEG frame from `/api/stream/mjpeg?n=1`, bounded by `io.LimitReader`, and — only if larger than the cap — decodes, downscales, and re-encodes. Enforces the memory rule: never decode above the cap.

**Files:**
- Modify: `internal/backend/public.go`
- Create: `internal/backend/public_test.go`

**Interfaces:**
- Consumes: `*nanokvm.Client` (for token), `Shot`, `ScreenshotOpts`.
- Produces: real `(*Public).Screenshot`. Const `maxDecodePixels = 2_100_000` (~1080p; 4K's ~8.3M pixels is refused).

- [ ] **Step 1: Write the failing test**

```go
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
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/backend/ -run TestPublicScreenshot`
Expected: FAIL (stub returns "not implemented"; oversize test may pass for the wrong reason — the small-frame test will fail).

- [ ] **Step 3: Implement the real `Screenshot` in `internal/backend/public.go`**

```go
package backend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"

	"golang.org/x/image/draw"

	"github.com/scgreenhalgh/nanokvm-mcp/internal/nanokvm"
)

type Public struct{ kvm *nanokvm.Client }

func NewPublic(kvm *nanokvm.Client) *Public { return &Public{kvm: kvm} }
func (p *Public) Name() string              { return "public" }

const (
	maxDecodePixels = 2_100_000 // ~1920x1080; refuse to decode anything larger
	maxFrameBytes   = 8 << 20   // bound the stream read at 8MB
)

func (p *Public) Screenshot(ctx context.Context, opts ScreenshotOpts) (Shot, error) {
	tok, err := p.kvm.Token(ctx)
	if err != nil {
		return Shot{}, err
	}
	// Reconstruct the base URL via a request through the client's transport.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.kvm.BaseURL()+"/api/stream/mjpeg?n=1", nil)
	if err != nil {
		return Shot{}, err
	}
	req.AddCookie(&http.Cookie{Name: "nano-kvm-token", Value: tok})
	resp, err := p.kvm.HTTP().Do(req)
	if err != nil {
		return Shot{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Shot{}, fmt.Errorf("public screenshot: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxFrameBytes))
	if err != nil {
		return Shot{}, err
	}
	jpegBytes, err := extractJPEG(raw)
	if err != nil {
		return Shot{}, err
	}

	// Inspect dimensions WITHOUT full decode.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(jpegBytes))
	if err != nil {
		return Shot{}, err
	}
	needResize := (opts.Width > 0 && cfg.Width > opts.Width) || (opts.Height > 0 && cfg.Height > opts.Height)
	if !needResize {
		return Shot{JPEG: jpegBytes, Width: cfg.Width, Height: cfg.Height}, nil
	}
	if cfg.Width*cfg.Height > maxDecodePixels {
		return Shot{}, fmt.Errorf("frame %dx%d exceeds decode cap (%d px); use the picoclaw backend for on-device resize",
			cfg.Width, cfg.Height, maxDecodePixels)
	}
	return resizeJPEG(jpegBytes, opts)
}

func extractJPEG(buf []byte) ([]byte, error) {
	start := bytes.Index(buf, []byte{0xFF, 0xD8})
	if start < 0 {
		return nil, errors.New("no JPEG SOI marker in stream")
	}
	end := bytes.Index(buf[start:], []byte{0xFF, 0xD9})
	if end < 0 {
		return nil, errors.New("no JPEG EOI marker in stream")
	}
	return buf[start : start+end+2], nil
}

func resizeJPEG(in []byte, opts ScreenshotOpts) (Shot, error) {
	src, err := jpeg.Decode(bytes.NewReader(in))
	if err != nil {
		return Shot{}, err
	}
	b := src.Bounds()
	nw, nh := b.Dx(), b.Dy()
	if opts.Width > 0 && nw > opts.Width {
		nh = nh * opts.Width / nw
		nw = opts.Width
	}
	if opts.Height > 0 && nh > opts.Height {
		nw = nw * opts.Height / nh
		nh = opts.Height
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	q := opts.Quality
	if q <= 0 {
		q = 80
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: q}); err != nil {
		return Shot{}, err
	}
	return Shot{JPEG: out.Bytes(), Width: nw, Height: nh}, nil
}
```

- [ ] **Step 3a: Add accessors to `nanokvm.Client` and the `x/image` dep**

Add to `internal/nanokvm/client.go`:

```go
// BaseURL returns the configured base URL (used by the public backend).
func (c *Client) BaseURL() string { return c.cfg.BaseURL }

// HTTP returns the underlying HTTP client (used by the public backend).
func (c *Client) HTTP() *http.Client { return c.http }
```

Then:

```bash
go get golang.org/x/image/draw
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/backend/ -run TestPublicScreenshot -v`
Expected: PASS (small frame passes through; oversize refused).

- [ ] **Step 5: Commit**

```bash
git add internal/backend/public.go internal/backend/public_test.go internal/nanokvm/client.go go.mod go.sum
git commit -m "feat(backend): public screenshot fallback with hard 1080p decode cap"
```

---

### Task 16: Public backend input (WebSocket HID + paste)

Replaces the stub `Input`. Text uses the REST paste API; other verbs use the WebSocket HID protocol with the modern `websocket` package (the Go equivalent of what broke in the Python fork — here we verify against a real in-process WS server). Coordinates are the same normalized `[0,1]` values, mapped to NanoKVM's `1..32767` range.

**Files:**
- Modify: `internal/backend/public.go`
- Create: `internal/hid/keycodes.go`, `internal/hid/keycodes_test.go`, `internal/backend/public_input_test.go`

**Interfaces:**
- Consumes: `*nanokvm.Client` (WSURL, Token), `Action`, `hid.KeyCode`.
- Produces:
  - `internal/hid`: `func Code(name string) (byte, bool)`, `func CharCode(r rune) (code byte, shift bool, ok bool)`. Tables derived from the USB HID Usage Tables spec (cited), not copied from upstream.
  - real `(*Public).Input`.

- [ ] **Step 1: Write the keycode test**

```go
package hid

import "testing"

func TestNamedKeys(t *testing.T) {
	for name, want := range map[string]byte{"enter": 0x28, "a": 0x04, "f1": 0x3A, "tab": 0x2B, "up": 0x52} {
		got, ok := Code(name)
		if !ok || got != want {
			t.Errorf("Code(%q)=%#x,%v want %#x", name, got, ok, want)
		}
	}
	if _, ok := Code("nope"); ok {
		t.Error("unknown key should not resolve")
	}
}

func TestCharCodes(t *testing.T) {
	c, shift, ok := CharCode('A')
	if !ok || c != 0x04 || !shift {
		t.Errorf("CharCode('A')=%#x shift=%v ok=%v", c, shift, ok)
	}
	c, shift, ok = CharCode('a')
	if !ok || c != 0x04 || shift {
		t.Errorf("CharCode('a')=%#x shift=%v ok=%v", c, shift, ok)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/hid/`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement `internal/hid/keycodes.go`**

```go
// Package hid holds USB HID keyboard usage codes.
//
// Codes are taken from the USB HID Usage Tables (HUT) spec, section 10
// (Keyboard/Keypad Page 0x07): https://usb.org/sites/default/files/hut1_22.pdf
// These are published facts, not code copied from any implementation.
package hid

var named = map[string]byte{
	"a": 0x04, "b": 0x05, "c": 0x06, "d": 0x07, "e": 0x08, "f": 0x09, "g": 0x0A,
	"h": 0x0B, "i": 0x0C, "j": 0x0D, "k": 0x0E, "l": 0x0F, "m": 0x10, "n": 0x11,
	"o": 0x12, "p": 0x13, "q": 0x14, "r": 0x15, "s": 0x16, "t": 0x17, "u": 0x18,
	"v": 0x19, "w": 0x1A, "x": 0x1B, "y": 0x1C, "z": 0x1D,
	"1": 0x1E, "2": 0x1F, "3": 0x20, "4": 0x21, "5": 0x22, "6": 0x23, "7": 0x24,
	"8": 0x25, "9": 0x26, "0": 0x27,
	"enter": 0x28, "return": 0x28, "escape": 0x29, "esc": 0x29, "backspace": 0x2A,
	"tab": 0x2B, "space": 0x2C, "minus": 0x2D, "equal": 0x2E,
	"f1": 0x3A, "f2": 0x3B, "f3": 0x3C, "f4": 0x3D, "f5": 0x3E, "f6": 0x3F,
	"f7": 0x40, "f8": 0x41, "f9": 0x42, "f10": 0x43, "f11": 0x44, "f12": 0x45,
	"insert": 0x49, "home": 0x4A, "pageup": 0x4B, "delete": 0x4C, "end": 0x4D,
	"pagedown": 0x4E, "right": 0x4F, "left": 0x50, "down": 0x51, "up": 0x52,
}

func Code(name string) (byte, bool) {
	c, ok := named[name]
	return c, ok
}

var shifted = map[rune]byte{
	'!': 0x1E, '@': 0x1F, '#': 0x20, '$': 0x21, '%': 0x22, '^': 0x23, '&': 0x24,
	'*': 0x25, '(': 0x26, ')': 0x27, '_': 0x2D, '+': 0x2E,
}

// CharCode returns the usage code and shift requirement for a printable rune.
func CharCode(r rune) (code byte, shift bool, ok bool) {
	if r >= 'a' && r <= 'z' {
		c, _ := Code(string(r))
		return c, false, true
	}
	if r >= 'A' && r <= 'Z' {
		c, _ := Code(string(r + 32))
		return c, true, true
	}
	if r >= '0' && r <= '9' {
		c, _ := Code(string(r))
		return c, false, true
	}
	if c, ok := shifted[r]; ok {
		return c, true, true
	}
	if r == ' ' {
		return 0x2C, false, true
	}
	return 0, false, false
}
```

- [ ] **Step 4: Run keycode test to verify it passes**

Run: `go test ./internal/hid/ -v`
Expected: PASS.

- [ ] **Step 5: Add the websocket dependency**

```bash
go get github.com/coder/websocket
```

- [ ] **Step 6: Write the public input test with a real in-process WS server**

```go
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
```

- [ ] **Step 7: Implement `Input` in `internal/backend/public.go`**

```go
// Appended to internal/backend/public.go

import (
	// ... existing imports plus:
	"encoding/json"
	"time"

	"github.com/coder/websocket"
	"github.com/scgreenhalgh/nanokvm-mcp/internal/hid"
)

func normToKVM(v float64) int {
	k := int(v*0x7FFE) + 1
	if k < 1 {
		k = 1
	}
	if k > 0x7FFF {
		k = 0x7FFF
	}
	return k
}

var mouseButton = map[string]int{"left": 0, "middle": 1, "right": 2, "": 0}

func (p *Public) Input(ctx context.Context, actions []Action) error {
	if err := ValidateActions(actions); err != nil {
		return err
	}
	// text-only fast path uses the REST paste API; no websocket needed.
	onlyText := true
	for _, a := range actions {
		if a.Action != "type" && a.Action != "wait" {
			onlyText = false
			break
		}
	}
	if onlyText {
		for _, a := range actions {
			if a.Action == "wait" {
				time.Sleep(time.Duration(a.DurationMs) * time.Millisecond)
				continue
			}
			if _, err := p.kvm.Do(ctx, http.MethodPost, "/api/hid/paste",
				map[string]any{"content": a.Text, "langue": ""}); err != nil {
				return err
			}
		}
		return nil
	}

	tok, err := p.kvm.Token(ctx)
	if err != nil {
		return err
	}
	c, _, err := websocket.Dial(ctx, p.kvm.WSURL(), &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": {"nano-kvm-token=" + tok}},
	})
	if err != nil {
		return err
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	send := func(msg []int) error {
		b, _ := json.Marshal(msg)
		return c.Write(ctx, websocket.MessageText, b)
	}

	for _, a := range actions {
		switch a.Action {
		case "wait":
			time.Sleep(time.Duration(a.DurationMs) * time.Millisecond)
		case "move":
			if err := send([]int{2, 3, 0, normToKVM(*a.X), normToKVM(*a.Y)}); err != nil {
				return err
			}
		case "click":
			btn := mouseButton[a.Button]
			if a.X != nil && a.Y != nil {
				if err := send([]int{2, 3, 0, normToKVM(*a.X), normToKVM(*a.Y)}); err != nil {
					return err
				}
			}
			if err := send([]int{2, 1, btn, 0, 0}); err != nil { // down
				return err
			}
			if err := send([]int{2, 2, 0, 0, 0}); err != nil { // up
				return err
			}
		case "scroll":
			if err := send([]int{2, 4, 0, 0, a.Amount}); err != nil {
				return err
			}
		case "type":
			for _, r := range a.Text {
				code, shift, ok := hid.CharCode(r)
				if !ok {
					continue
				}
				sh := 0
				if shift {
					sh = 2
				}
				if err := send([]int{1, int(code), 0, sh, 0, 0}); err != nil {
					return err
				}
				if err := send([]int{1, 0, 0, 0, 0, 0}); err != nil {
					return err
				}
			}
		case "hotkey":
			var mod int
			var last byte
			for _, k := range a.Keys {
				switch k {
				case "ctrl":
					mod |= 1
				case "shift":
					mod |= 2
				case "alt":
					mod |= 4
				case "meta", "cmd", "win", "super":
					mod |= 8
				default:
					if code, ok := hid.Code(k); ok {
						last = code
					}
				}
			}
			if err := send([]int{1, int(last), mod & 1, (mod >> 1) & 1, (mod >> 2) & 1, (mod >> 3) & 1}); err != nil {
				return err
			}
			if err := send([]int{1, 0, 0, 0, 0, 0}); err != nil {
				return err
			}
		case "drag":
			if a.From == nil || a.To == nil {
				return fmt.Errorf("drag requires from and to")
			}
			if err := send([]int{2, 3, 0, normToKVM(*a.From.X), normToKVM(*a.From.Y)}); err != nil {
				return err
			}
			if err := send([]int{2, 1, 0, 0, 0}); err != nil {
				return err
			}
			if err := send([]int{2, 3, 0, normToKVM(*a.To.X), normToKVM(*a.To.Y)}); err != nil {
				return err
			}
			if err := send([]int{2, 2, 0, 0, 0}); err != nil {
				return err
			}
		}
	}
	return nil
}
```

Note: the WS message shapes (`[1, keycode, ctrl, shift, alt, meta]` for keyboard, `[2, event, button, x, y]` for mouse) mirror the protocol the Python fork used; verify against upstream `server/router/ws.go` and `server/service/hid` if a message is rejected on real hardware. The `hotkey` modifier packing sends left-modifier bits.

- [ ] **Step 8: Run to verify it passes**

Run: `go test ./internal/backend/ -run TestPublicInput -v`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/hid/ internal/backend/public.go internal/backend/public_input_test.go go.mod go.sum
git commit -m "feat(backend): public input fallback via paste API and WebSocket HID"
```

---

### Task 17: Deploy artifacts and README

**Files:**
- Create: `deploy/S96nanokvm-mcp`, `deploy/install.sh`, `README.md`

**Interfaces:**
- Consumes: the built binary.
- Produces: an init script and installer matching the confirmed device layout.

- [ ] **Step 1: Write `deploy/S96nanokvm-mcp` (BusyBox init script)**

```sh
#!/bin/sh
# NanoKVM MCP sidecar init script. Install as /etc/init.d/S96nanokvm-mcp.
DAEMON=/root/nanokvm-mcp/nanokvm-mcp
CONF=/root/nanokvm-mcp/nanokvm-mcp.env
PIDFILE=/var/run/nanokvm-mcp.pid
LOG=/data/nanokvm-mcp/daemon.log

start() {
	printf 'Starting nanokvm-mcp: '
	# shellcheck disable=SC1090
	[ -f "$CONF" ] && . "$CONF"
	export NANOKVM_HOST NANOKVM_USER NANOKVM_PASS NANOKVM_MCP_BIND \
		NANOKVM_MCP_TOKEN NANOKVM_MCP_READONLY NANOKVM_MCP_AUDIT \
		NANOKVM_MCP_AUDIT_FULL GOMEMLIMIT
	: "${GOMEMLIMIT:=24MiB}"
	export GOMEMLIMIT
	mkdir -p /data/nanokvm-mcp
	start-stop-daemon -S -q -b -m -p "$PIDFILE" -x "$DAEMON" >>"$LOG" 2>&1
	echo OK
}
stop() {
	printf 'Stopping nanokvm-mcp: '
	start-stop-daemon -K -q -p "$PIDFILE"
	echo OK
}
case "$1" in
	start) start ;;
	stop) stop ;;
	restart) stop; sleep 1; start ;;
	*) echo "Usage: $0 {start|stop|restart}"; exit 1 ;;
esac
```

- [ ] **Step 2: Write `deploy/install.sh`**

```sh
#!/bin/sh
# Install the sidecar on a NanoKVM. Run from a machine with scp/ssh access.
# Usage: HOST=root@nanokvm ./deploy/install.sh
set -e
: "${HOST:?set HOST=root@<nanokvm>}"
SSH="ssh -o PubkeyAuthentication=no -o PreferredAuthentications=password"
SCP="scp -o PubkeyAuthentication=no -o PreferredAuthentications=password"

echo "Building riscv64 binary..."
mise run build

echo "Creating directories..."
$SSH "$HOST" 'mkdir -p /root/nanokvm-mcp /data/nanokvm-mcp'

echo "Copying binary and init script..."
$SCP dist/nanokvm-mcp "$HOST":/root/nanokvm-mcp/nanokvm-mcp
$SCP deploy/S96nanokvm-mcp "$HOST":/etc/init.d/S96nanokvm-mcp
$SSH "$HOST" 'chmod +x /root/nanokvm-mcp/nanokvm-mcp /etc/init.d/S96nanokvm-mcp'

echo "Writing config template if absent..."
$SSH "$HOST" 'test -f /root/nanokvm-mcp/nanokvm-mcp.env || cat > /root/nanokvm-mcp/nanokvm-mcp.env <<EOF
NANOKVM_HOST=127.0.0.1
NANOKVM_MCP_BIND=127.0.0.1:8080
# NANOKVM_MCP_TOKEN=   # set a fixed bearer token, or read the generated one from the log
# NANOKVM_MCP_READONLY=true
EOF'

echo "Starting..."
$SSH "$HOST" '/etc/init.d/S96nanokvm-mcp restart'
echo "Done. Bearer token (if generated) is in /data/nanokvm-mcp/daemon.log"
```

- [ ] **Step 3: Write `README.md`**

Include: what it is; the GPL-3.0 notice and upstream attribution; the confirmed install layout; environment variables (`NANOKVM_HOST`, `NANOKVM_USER`, `NANOKVM_PASS`, `NANOKVM_HTTPS`, `NANOKVM_VERIFY_SSL`, `NANOKVM_MCP_BIND`, `NANOKVM_MCP_TOKEN`, `NANOKVM_MCP_READONLY`, `NANOKVM_MCP_AUDIT`, `NANOKVM_MCP_AUDIT_FULL`, `GOMEMLIMIT`); the recommended Tailscale exposure; the MCP client config snippet pointing at `http://<device>:8080/` with the bearer token; and the security model (loopback default, bearer auth, read-only mode, audit log with redaction). Reference `docs/superpowers/specs/2026-07-22-nanokvm-mcp-sidecar-design.md`.

- [ ] **Step 4: Verify the scripts are valid shell**

Run: `sh -n deploy/S96nanokvm-mcp && sh -n deploy/install.sh && echo OK`
Expected: `OK`.

- [ ] **Step 5: Commit**

```bash
git add deploy/ README.md
git commit -m "docs: deploy init script, installer, and README"
```

---

## Device validation (manual, after Task 17)

Not a code task, but the acceptance gate. On the device:

- [ ] `mise run build && HOST=root@<nanokvm> ./deploy/install.sh`
- [ ] Confirm the daemon starts and logs `backend=picoclaw`.
- [ ] From the client machine, add the MCP server (`http://<device>:8080/`, bearer token) and confirm `tools/list` returns 14 tools (7 in read-only mode).
- [ ] `nanokvm_screenshot` returns an image; `nanokvm_led_status` shows `hdd_available:false` on this beta board.
- [ ] `nanokvm_input` with a `wait` action succeeds (no HID side effects), then a real `type`/`click` against a test target.
- [ ] Check resident memory: `ps` / `cat /proc/<pid>/status | grep VmRSS` is under 25 MB.
- [ ] Trigger a firmware app update (or simulate by moving `/kvmapp`) and confirm `/root/nanokvm-mcp/` survives.

---

## Self-Review

**Spec coverage:**
- On-device Go daemon, static riscv64, no CGO → Task 1 (build), Task 17 (deploy). ✓
- GPL-3.0 + attribution → Task 1 (LICENSE), Tasks 3/16 (origin headers). ✓
- Config + token → Task 2. ✓
- CryptoJS auth → Task 3. ✓
- Re-auth on expiry (fixes Python bug) → Task 4. ✓
- No mocked transport → Tasks 4–16 all use `httptest`/in-process WS. ✓
- VM + storage endpoints, beta HDD-LED handling → Tasks 5–6. ✓
- Backend interface, picoclaw (no decode), public (capped decode), selection → Tasks 7–9, 15–16. ✓
- 14 annotated tools, batched input, read-only filter → Task 12. ✓
- Bearer auth, loopback default bind → Tasks 11, 2, 13. ✓
- Audit log with default text redaction → Task 10, wired in Task 12. ✓
- apicheck drift guard → Task 14. ✓
- Memory: GOMEMLIMIT, no-4K-decode, size/RSS gates → Task 1 (size), Task 15 (cap), Task 17 (GOMEMLIMIT), device validation (RSS). ✓
- Install layout `/root/nanokvm-mcp` + `/data/nanokvm-mcp` → Task 17, device validation. ✓

**Placeholder scan:** No TBD/TODO. The Task 13 stub for `NewPublic` is explicit, created in-task, and replaced in Tasks 15–16 — not a placeholder. README content (Task 17 Step 3) is specified by required contents rather than full prose, which is appropriate for a docs file.

**Type consistency:** `KVMBackend` (`Name`/`Screenshot`/`Input`), `Action`, `Shot`, `ScreenshotOpts` are defined in Task 7 and used unchanged in Tasks 8, 9, 12, 15, 16. `nanokvm.Client` methods defined in Tasks 4–6 are consumed with matching signatures in Task 12. `audit.Logger.Record` signature matches its Task 12 call site. Tool names are identical between Task 12 and the apicheck/README references.

**SDK-verification caveats flagged inline:** in-memory transport / `ListTools` helper names (Task 12) and picoclaw path-constant matching (Task 14) each carry a note to confirm against the installed SDK/upstream and adjust the harness without changing behavior under test.
