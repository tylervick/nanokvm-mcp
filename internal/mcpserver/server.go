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
