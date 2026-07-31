package nanokvm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

// gpioHandler mirrors upstream's /api/vm/gpio (server/service/vm/gpio.go).
//
// SetGpio binds proto.SetGpioReq, which carries no json tags at all and relies
// on encoding/json's case-insensitive fallback to field names -- so the request
// struct here is deliberately untagged too, and a body whose keys do not reach
// Type is answered with code -2 ("invalid power event") on HTTP 200. GetGpio
// answers proto.GetGpioRsp on the same path.
func gpioHandler(led map[string]any) func(*http.Request) (any, int) {
	return func(r *http.Request) (any, int) {
		if r.Method == http.MethodGet {
			return led, 0
		}
		var req struct {
			Type     string
			Duration uint
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return nil, -1 // "invalid arguments"
		}
		if req.Type != "power" && req.Type != "reset" {
			return nil, -2
		}
		return nil, 0
	}
}

func TestPowerSendsTheFieldsUpstreamBinds(t *testing.T) {
	f := newFakeKVM()
	defer f.Close()
	f.onRequest("/api/vm/gpio", gpioHandler(nil))
	c := newTestClient(f)

	if err := c.Power(context.Background(), "power", 800); err != nil {
		t.Fatalf("Power: %v", err)
	}
	body := f.body(t, "/api/vm/gpio")
	if body["type"] != "power" {
		t.Errorf("type = %v, want power (upstream binds SetGpioReq.Type)", body["type"])
	}
	if body["duration"] != float64(800) {
		t.Errorf("duration = %v, want 800 (upstream binds SetGpioReq.Duration)", body["duration"])
	}
}

// Upstream rejects an unknown event with an envelope code on HTTP 200. This is
// also what a request whose keys never reach SetGpioReq.Type looks like, so a
// silent success here would hide a wire-shape regression.
func TestPowerReportsAnUnknownEventRejection(t *testing.T) {
	f := newFakeKVM()
	defer f.Close()
	f.onRequest("/api/vm/gpio", gpioHandler(nil))
	c := newTestClient(f)

	if err := c.Power(context.Background(), "nope", 800); err == nil {
		t.Error("Power reported success after the firmware rejected the event")
	}
}

// hdd_available is ours, not the firmware's: upstream's GetGpioRsp carries only
// pwr and hdd. It is derived from the hardware version, so a firmware variant
// that happened to send the key must not decide it.
func TestLEDStatusDerivesHDDAvailabilityRatherThanReadingIt(t *testing.T) {
	f := newFakeKVM()
	defer f.Close()
	f.onRequest("/api/vm/gpio", gpioHandler(map[string]any{
		"pwr": true, "hdd": true, "hdd_available": true,
	}))
	f.on("/api/vm/hardware", map[string]any{"version": "beta"})
	c := newTestClient(f)

	led, err := c.LEDStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if led.HDDAvailable {
		t.Error("HDD availability was taken from the payload; beta hardware has no HDD LED")
	}
}

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
