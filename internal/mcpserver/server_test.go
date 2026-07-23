package mcpserver

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/scgreenhalgh/nanokvm-mcp/internal/audit"
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

func TestInputHandlerRedactsAuditText(t *testing.T) {
	var buf bytes.Buffer
	aud := audit.New(&buf, false) // default = redact
	s := New(Deps{Backend: nopBackend{}, Audit: aud, ReadOnly: false})

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

	_, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "nanokvm_input",
		Arguments: map[string]any{"actions": []map[string]any{{"action": "type", "text": "s3cretPW"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "s3cretPW") {
		t.Errorf("secret text leaked into audit log: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "len=8") {
		t.Errorf("expected redaction marker for 8-char secret, got: %s", buf.String())
	}
}
