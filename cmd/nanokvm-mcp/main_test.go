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
