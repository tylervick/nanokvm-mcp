package nanokvm

import (
	"context"
	"net/http"
)

// pasteReq is the body of POST /api/hid/paste.
//
// Upstream binds hid.PasteReq, which carries `form` tags only; gin selects the
// JSON binder from our Content-Type, and encoding/json then matches these names
// against PasteReq's field names case-insensitively. Langue selects the keymap
// (LangueSwitch): empty means upstream's base map, so it is always sent rather
// than omitted.
type pasteReq struct {
	Content string `json:"content"`
	Langue  string `json:"langue"`
}

// Paste types text on the target through the firmware's paste endpoint, which
// is cheaper than driving the HID websocket a character at a time.
func (c *Client) Paste(ctx context.Context, content, langue string) error {
	_, err := c.Do(ctx, http.MethodPost, "/api/hid/paste", pasteReq{Content: content, Langue: langue})
	return err
}
