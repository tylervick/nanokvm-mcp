// Package backend abstracts screen capture and HID input, which have both a
// preferred on-device implementation (picoclaw) and a public-API fallback.
package backend

import (
	"context"
	"fmt"
)

type Point struct {
	X *float64 `json:"x"`
	Y *float64 `json:"y"`
}

type Action struct {
	Action     string   `json:"action"`
	X          *float64 `json:"x,omitempty"`
	Y          *float64 `json:"y,omitempty"`
	From       *Point   `json:"from,omitempty"`
	To         *Point   `json:"to,omitempty"`
	Button     string   `json:"button,omitempty"`
	Text       string   `json:"text,omitempty"`
	Keys       []string `json:"keys,omitempty"`
	Direction  string   `json:"direction,omitempty"`
	Amount     int      `json:"amount,omitempty"`
	DurationMs int      `json:"duration_ms,omitempty"`
}

type ScreenshotOpts struct {
	Width   int
	Height  int
	Quality int
}

type Shot struct {
	JPEG   []byte
	Width  int
	Height int
}

type KVMBackend interface {
	Name() string
	Screenshot(ctx context.Context, opts ScreenshotOpts) (Shot, error)
	Input(ctx context.Context, actions []Action) error
}

var validVerbs = map[string]bool{
	"click": true, "move": true, "type": true, "hotkey": true,
	"scroll": true, "drag": true, "wait": true,
}

func inRange(p *float64) bool { return p == nil || (*p >= 0 && *p <= 1) }

// ValidateActions rejects unknown verbs and normalized coordinates outside [0,1].
func ValidateActions(actions []Action) error {
	if len(actions) == 0 {
		return fmt.Errorf("no actions supplied")
	}
	for i, a := range actions {
		if !validVerbs[a.Action] {
			return fmt.Errorf("action %d: unknown verb %q", i, a.Action)
		}
		if !inRange(a.X) || !inRange(a.Y) {
			return fmt.Errorf("action %d: coordinates must be normalized to [0,1]", i)
		}
		if a.From != nil && (!inRange(a.From.X) || !inRange(a.From.Y)) {
			return fmt.Errorf("action %d: from coordinates must be normalized to [0,1]", i)
		}
		if a.To != nil && (!inRange(a.To.X) || !inRange(a.To.Y)) {
			return fmt.Errorf("action %d: to coordinates must be normalized to [0,1]", i)
		}
		switch a.Action {
		case "move":
			if a.X == nil || a.Y == nil {
				return fmt.Errorf("action %d: move requires x and y", i)
			}
		case "drag":
			if a.From == nil || a.To == nil ||
				a.From.X == nil || a.From.Y == nil ||
				a.To.X == nil || a.To.Y == nil {
				return fmt.Errorf("action %d: drag requires from and to, each with x and y", i)
			}
		}
	}
	return nil
}
