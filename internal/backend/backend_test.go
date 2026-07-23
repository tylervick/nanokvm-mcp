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
}
