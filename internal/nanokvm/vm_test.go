package nanokvm

import (
	"context"
	"errors"
	"sync"
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

// TestLEDStatusConcurrent exercises LEDStatus from many goroutines at once so
// that `go test -race` can catch data races on the cached hardware version
// (guarded by Client.mu; see internal/nanokvm/vm.go).
func TestLEDStatusConcurrent(t *testing.T) {
	f := newFakeKVM()
	defer f.Close()
	f.on("/api/vm/gpio", map[string]bool{"pwr": true, "hdd": false})
	f.on("/api/vm/hardware", map[string]any{"version": "alpha"})
	c := newTestClient(f)

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			led, err := c.LEDStatus(context.Background())
			if err != nil {
				errs <- err
				return
			}
			if !led.HDDAvailable {
				errs <- errors.New("HDD LED should be reported available on alpha hardware")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestResetHIDPostsHidReset(t *testing.T) {
	f := newFakeKVM()
	defer f.Close()
	called := false
	f.onFunc("/api/hid/reset", func() (any, int) {
		called = true
		return map[string]any{}, 0
	})

	c := New(ClientConfig{BaseURL: f.URL, Username: "u", Password: "p"})
	if err := c.ResetHID(context.Background()); err != nil {
		t.Fatalf("ResetHID: %v", err)
	}
	if !called {
		t.Error("ResetHID did not hit /api/hid/reset")
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
