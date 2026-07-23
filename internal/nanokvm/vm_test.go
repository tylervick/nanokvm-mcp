package nanokvm

import (
	"context"
	"testing"
	"time"
)

func TestLEDStatusMarksHDDUnavailableOnBeta(t *testing.T) {
	f := newFakeKVM()
	defer f.Close()
	f.on("/api/vm/gpio", map[string]bool{"pwr": true, "hdd": false})
	f.on("/api/vm/hardware", map[string]any{"version": "beta"})
	c := newTestClient(f)

	hw, err := c.Hardware(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	led, err := c.LEDStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !led.PWR {
		t.Error("pwr should be true")
	}
	// Non-alpha hardware has no HDD LED; report unavailable rather than a fake reading.
	if hw.Version != "beta" {
		t.Fatalf("hw version: %q", hw.Version)
	}
	if led.HDDAvailable {
		t.Error("HDD LED should be reported unavailable on beta hardware")
	}
}

func TestPowerCycleSequences(t *testing.T) {
	f := newFakeKVM()
	defer f.Close()
	f.on("/api/vm/gpio", map[string]any{})
	c := newTestClient(f)

	var slept time.Duration
	if err := c.PowerCycle(context.Background(), 3000, func(d time.Duration) { slept = d }); err != nil {
		t.Fatal(err)
	}
	if slept != 3000*time.Millisecond {
		t.Errorf("power cycle should wait 3000ms, waited %v", slept)
	}
}
