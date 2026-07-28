package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// freePort reserves an ephemeral port and releases it for the server to bind.
// Racier than passing a listener down, but run() owns its own bind; the window
// is tiny and beats the old fixed :8199, which flaked on port collisions.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// startServer launches run(ctx) on a free port and waits until it serves.
func startServer(t *testing.T, ctx context.Context) (addr string, errCh chan error) {
	t.Helper()
	addr = freePort(t)
	t.Setenv("NANOKVM_HOST", "127.0.0.1")
	t.Setenv("NANOKVM_MCP_TOKEN", "smoke-token")
	t.Setenv("NANOKVM_MCP_BIND", addr)
	t.Setenv("NANOKVM_MCP_AUDIT", t.TempDir()+"/audit.log")
	t.Setenv("NANOKVM_MCP_READONLY", "true") // avoid needing a backend probe

	errCh = make(chan error, 1)
	go func() { errCh <- run(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return addr, errCh
		}
		select {
		case err := <-errCh:
			t.Fatalf("run() exited during startup: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatalf("server never came up on %s", addr)
	return "", nil
}

func TestServerRejectsUnauthed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	addr, errCh := startServer(t, ctx)
	// Join the server goroutine before t.Setenv cleanups run, so it can't
	// overlap the next test's environment or listener.
	defer func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(5 * time.Second):
			t.Error("run() did not exit after cancellation")
		}
	}()

	// No Authorization header -> 401.
	resp, err := http.Post(fmt.Sprintf("http://%s/", addr), "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("want 401 without bearer, got %d", resp.StatusCode)
	}
}

func TestServerReportsBindFailure(t *testing.T) {
	// Occupy a port so ListenAndServe fails immediately.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	t.Setenv("NANOKVM_HOST", "127.0.0.1")
	t.Setenv("NANOKVM_MCP_TOKEN", "smoke-token")
	t.Setenv("NANOKVM_MCP_BIND", l.Addr().String())
	t.Setenv("NANOKVM_MCP_AUDIT", t.TempDir()+"/audit.log")
	t.Setenv("NANOKVM_MCP_READONLY", "true")

	errCh := make(chan error, 1)
	go func() { errCh <- run(context.Background()) }()

	select {
	case err := <-errCh:
		if err == nil {
			t.Error("run() should return the bind error, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run() did not return after failing to bind")
	}
}

func TestServerShutsDownCleanlyOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	_, errCh := startServer(t, ctx)

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("run() should return nil on graceful shutdown, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run() did not return within 5s of cancellation")
	}
}
