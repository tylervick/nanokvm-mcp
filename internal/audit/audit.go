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

// normalize converts args (which may contain typed structs such as
// []backend.Action) into generic map/slice/scalar shapes via a JSON round-trip,
// so redact reaches nested "text" fields regardless of the concrete Go type
// passed in. On marshal/unmarshal failure it returns a non-leaking marker
// rather than risk emitting unredacted data.
func normalize(args map[string]any) any {
	b, err := json.Marshal(args)
	if err != nil {
		return map[string]any{"redaction_error": "marshal failed"}
	}
	var generic any
	if err := json.Unmarshal(b, &generic); err != nil {
		return map[string]any{"redaction_error": "unmarshal failed"}
	}
	return generic
}

func (l *Logger) Record(tool, backend string, args map[string]any, err error) {
	a := any(args)
	if !l.full {
		a = l.redact(normalize(args))
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
