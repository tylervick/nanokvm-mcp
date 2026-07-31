package nanokvm

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// pasteHandler mirrors upstream's acceptance rule for /api/hid/paste
// (server/service/hid/paste.go): the handler binds PasteReq and runs it through
// proto.ParseFormRequest, whose validator marks Content as required. A body
// that does not carry a readable content field is answered with code -1
// ("invalid arguments") on HTTP 200, not with a 4xx.
func pasteHandler(f *fakeKVM) func(*http.Request) (any, int) {
	return func(r *http.Request) (any, int) {
		var req struct {
			Content string `json:"content"`
			Langue  string `json:"langue"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return nil, -1
		}
		if req.Content == "" {
			return nil, -1 // validate:"required"
		}
		return nil, 0
	}
}

func TestPasteSendsTheFieldsUpstreamBinds(t *testing.T) {
	f := newFakeKVM()
	defer f.Close()
	f.onRequest("/api/hid/paste", pasteHandler(f))
	c := newTestClient(f)

	if err := c.Paste(context.Background(), "hello", ""); err != nil {
		t.Fatalf("Paste: %v", err)
	}
	body := f.body(t, "/api/hid/paste")
	if body["content"] != "hello" {
		t.Errorf("content = %v, want hello (upstream binds PasteReq.Content)", body["content"])
	}
	if _, ok := body["langue"]; !ok {
		t.Error("langue missing; upstream's LangueSwitch reads it and an absent key means the base keymap")
	}
}

// Upstream answers a rejected paste with HTTP 200 and a non-zero envelope code.
// Reporting success there is how a keystroke that never reached the target gets
// reported as delivered.
func TestPasteReportsAFirmwareRejection(t *testing.T) {
	f := newFakeKVM()
	defer f.Close()
	f.onRequest("/api/hid/paste", pasteHandler(f))
	c := newTestClient(f)

	if err := c.Paste(context.Background(), "", ""); err == nil {
		t.Error("Paste reported success after the firmware rejected the request")
	}
}
