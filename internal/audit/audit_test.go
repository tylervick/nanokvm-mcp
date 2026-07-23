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
