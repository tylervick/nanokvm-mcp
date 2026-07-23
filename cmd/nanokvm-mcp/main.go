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
