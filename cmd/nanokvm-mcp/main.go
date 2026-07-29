// Command nanokvm-mcp is an MCP server for Sipeed NanoKVM devices.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tylervick/nanokvm-mcp/internal/audit"
	"github.com/tylervick/nanokvm-mcp/internal/backend"
	"github.com/tylervick/nanokvm-mcp/internal/config"
	"github.com/tylervick/nanokvm-mcp/internal/httpauth"
	"github.com/tylervick/nanokvm-mcp/internal/mcpserver"
	"github.com/tylervick/nanokvm-mcp/internal/nanokvm"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
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

	transport := &http.Transport{}
	if cfg.UseHTTPS {
		//nolint:gosec // opt-in: NANOKVM_VERIFY_SSL=false is for self-signed device certs, default is verification ON.
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: !cfg.VerifySSL}
	}
	httpClient := &http.Client{Transport: transport}

	kvm := nanokvm.New(nanokvm.ClientConfig{
		BaseURL:    baseURL,
		WSURL:      wsScheme + "://" + cfg.Host + "/api/ws",
		Username:   cfg.Username,
		Password:   cfg.Password,
		HTTPClient: httpClient,
	})

	// Audit log.
	if err := os.MkdirAll(filepath.Dir(cfg.AuditPath), 0o750); err != nil {
		log.Printf("audit dir: %v (logging to stderr)", err)
	}
	auditW := os.Stderr
	var auditFile *os.File
	if f, err := os.OpenFile(cfg.AuditPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		auditW, auditFile = f, f
	} else {
		log.Printf("audit file: %v (logging to stderr)", err)
	}
	aud := audit.New(auditW, cfg.AuditFull)

	// Backend selection.
	pub := backend.NewPublic(kvm) // Task 15
	be, err := backend.Select(ctx, backend.Deps{
		BaseURL:   baseURL,
		TokenPath: backend.DefaultTokenPath,
		SessionID: "nanokvm-mcp",
		HTTP:      httpClient,
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
	server := &http.Server{
		Addr:              cfg.BindAddr,
		Handler:           authed,
		ReadHeaderTimeout: 10 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()

	select {
	case err := <-serveErr:
		if auditFile != nil {
			_ = auditFile.Close()
		}
		return err
	case <-ctx.Done():
		log.Printf("shutting down")
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := server.Shutdown(sctx)
		if errors.Is(err, context.DeadlineExceeded) {
			err = nil // in-flight requests were cut off; still a deliberate stop
		}
		// Close the audit log only after Shutdown so in-flight mutating
		// calls still get their audit lines.
		if auditFile != nil {
			if cerr := auditFile.Close(); err == nil {
				err = cerr
			}
		}
		return err
	}
}
