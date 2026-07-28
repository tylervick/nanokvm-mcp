package nanokvm

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
)

// A data payload of the wrong JSON shape used to be swallowed silently,
// returning zero values with no trace. It must at least be logged.
func TestMalformedDataPayloadIsLogged(t *testing.T) {
	f := newFakeKVM()
	defer f.Close()
	// Info expects an object; serve a bare string.
	f.on("/api/vm/info", "not-an-object")

	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	c := New(ClientConfig{BaseURL: f.URL, Username: "u", Password: "p"})
	m, err := c.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("expected empty map for malformed payload, got %v", m)
	}
	if !strings.Contains(buf.String(), "/api/vm/info") {
		t.Errorf("malformed payload not logged with its path; log: %q", buf.String())
	}
}
