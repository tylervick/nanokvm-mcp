package backend

import "testing"

func f64(v float64) *float64 { return &v }

func TestValidateActions(t *testing.T) {
	if err := ValidateActions([]Action{{Action: "click", X: f64(0.5), Y: f64(0.5)}}); err != nil {
		t.Errorf("valid click rejected: %v", err)
	}
	if err := ValidateActions([]Action{{Action: "warp"}}); err == nil {
		t.Error("unknown verb should be rejected")
	}
	if err := ValidateActions([]Action{{Action: "move", X: f64(1.5), Y: f64(0.5)}}); err == nil {
		t.Error("out-of-range coord should be rejected")
	}
	if err := ValidateActions([]Action{{Action: "type", Text: "hi"}}); err != nil {
		t.Errorf("valid type rejected: %v", err)
	}
	if err := ValidateActions([]Action{}); err == nil {
		t.Error("empty action list should be rejected")
	}
	if err := ValidateActions([]Action{{Action: "drag", From: &Point{X: f64(1.5), Y: f64(0.5)}, To: &Point{X: f64(0.5), Y: f64(0.5)}}}); err == nil {
		t.Error("out-of-range nested From coordinate should be rejected")
	}
	if err := ValidateActions([]Action{{Action: "move"}}); err == nil {
		t.Error("move without x/y should be rejected")
	}
	if err := ValidateActions([]Action{{Action: "move", X: f64(0.5)}}); err == nil {
		t.Error("move with only x should be rejected")
	}
	if err := ValidateActions([]Action{{Action: "drag", From: &Point{}, To: &Point{}}}); err == nil {
		t.Error("drag with empty points should be rejected")
	}
	if err := ValidateActions([]Action{{Action: "move", X: f64(0.5), Y: f64(0.5)}}); err != nil {
		t.Errorf("valid move should be accepted: %v", err)
	}
}

func TestValidateActionsMouseButtons(t *testing.T) {
	for _, btn := range []string{"", "left", "middle", "right"} {
		if err := ValidateActions([]Action{{Action: "click", Button: btn}}); err != nil {
			t.Errorf("button %q should be accepted: %v", btn, err)
		}
	}
	// A typo'd button must not silently become a left click.
	if err := ValidateActions([]Action{{Action: "click", Button: "midle"}}); err == nil {
		t.Error("unknown mouse button should be rejected")
	}
	if err := ValidateActions([]Action{{Action: "click", Button: "Left"}}); err == nil {
		t.Error("button names are lowercase; 'Left' should be rejected, not coerced")
	}
}

func TestValidateActionsWaitCap(t *testing.T) {
	if err := ValidateActions([]Action{{Action: "wait", DurationMs: 500}}); err != nil {
		t.Errorf("short wait should be accepted: %v", err)
	}
	if err := ValidateActions([]Action{{Action: "wait", DurationMs: MaxWaitMs}}); err != nil {
		t.Errorf("wait at the cap should be accepted: %v", err)
	}
	if err := ValidateActions([]Action{{Action: "wait", DurationMs: MaxWaitMs + 1}}); err == nil {
		t.Error("wait above the cap should be rejected")
	}
	if err := ValidateActions([]Action{{Action: "wait", DurationMs: -1}}); err == nil {
		t.Error("negative wait should be rejected")
	}
}
