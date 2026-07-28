package nanokvm

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"
)

// logUnmarshal reports a data payload that didn't match the expected shape.
// Callers deliberately tolerate it (returning zero values) because firmware
// variants differ, but it must not be invisible.
func logUnmarshal(path string, err error) {
	if err != nil {
		log.Printf("nanokvm: %s: unexpected data shape: %v", path, err)
	}
}

type LED struct {
	PWR          bool `json:"pwr"`
	HDD          bool `json:"hdd"`
	HDDAvailable bool `json:"hdd_available"`
}

type Hardware struct {
	Version string
	Raw     map[string]any
}

func (c *Client) Power(ctx context.Context, action string, durationMs int) error {
	_, err := c.Do(ctx, http.MethodPost, "/api/vm/gpio",
		map[string]any{"type": action, "duration": durationMs})
	return err
}

func (c *Client) PowerCycle(ctx context.Context, offMs int, sleep func(time.Duration)) error {
	if sleep == nil {
		sleep = time.Sleep
	}
	if err := c.Power(ctx, "power", 5000); err != nil { // force off
		return err
	}
	sleep(time.Duration(offMs) * time.Millisecond)
	return c.Power(ctx, "power", 800) // power on
}

func (c *Client) Hardware(ctx context.Context) (Hardware, error) {
	raw, err := c.Do(ctx, http.MethodGet, "/api/vm/hardware", nil)
	if err != nil {
		return Hardware{}, err
	}
	var m map[string]any
	logUnmarshal("/api/vm/hardware", json.Unmarshal(raw, &m))
	v, _ := m["version"].(string)
	v = strings.ToLower(v)
	c.mu.Lock()
	c.hwVersion = v
	c.mu.Unlock()
	return Hardware{Version: v, Raw: m}, nil
}

func (c *Client) LEDStatus(ctx context.Context) (LED, error) {
	raw, err := c.Do(ctx, http.MethodGet, "/api/vm/gpio", nil)
	if err != nil {
		return LED{}, err
	}
	var led LED
	logUnmarshal("/api/vm/gpio", json.Unmarshal(raw, &led))
	// HDD LED exists only on alpha hardware (upstream gpio.go hardcodes hdd=false
	// otherwise). Report availability so the tool does not present a fake reading.
	c.mu.Lock()
	hw := c.hwVersion
	c.mu.Unlock()
	if hw == "" {
		if h, err := c.Hardware(ctx); err == nil {
			hw = h.Version
		}
	}
	led.HDDAvailable = hw == "alpha"
	return led, nil
}

func (c *Client) HDMIStatus(ctx context.Context) (map[string]any, error) {
	raw, err := c.Do(ctx, http.MethodGet, "/api/vm/hdmi", nil)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	logUnmarshal("/api/vm/hdmi", json.Unmarshal(raw, &m))
	return m, nil
}

func (c *Client) HDMIReset(ctx context.Context) error {
	_, err := c.Do(ctx, http.MethodPost, "/api/vm/hdmi/reset", nil)
	return err
}

func (c *Client) Info(ctx context.Context) (map[string]any, error) {
	raw, err := c.Do(ctx, http.MethodGet, "/api/vm/info", nil)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	logUnmarshal("/api/vm/info", json.Unmarshal(raw, &m))
	return m, nil
}
