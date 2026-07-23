package nanokvm

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

type LED struct {
	PWR          bool `json:"pwr"`
	HDD          bool `json:"hdd"`
	HDDAvailable bool `json:"hdd_available"`
}

type Hardware struct {
	Version string
	Raw     map[string]any
}

var hwOnce sync.Once
var hwVersion string

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
	_ = json.Unmarshal(raw, &m)
	v, _ := m["version"].(string)
	hwOnce.Do(func() { hwVersion = strings.ToLower(v) })
	return Hardware{Version: strings.ToLower(v), Raw: m}, nil
}

func (c *Client) LEDStatus(ctx context.Context) (LED, error) {
	raw, err := c.Do(ctx, http.MethodGet, "/api/vm/gpio", nil)
	if err != nil {
		return LED{}, err
	}
	var led LED
	_ = json.Unmarshal(raw, &led)
	// HDD LED exists only on alpha hardware (upstream gpio.go hardcodes hdd=false
	// otherwise). Report availability so the tool does not present a fake reading.
	if hwVersion == "" {
		if hw, err := c.Hardware(ctx); err == nil {
			hwVersion = hw.Version
		}
	}
	led.HDDAvailable = hwVersion == "alpha"
	return led, nil
}

func (c *Client) HDMIStatus(ctx context.Context) (map[string]any, error) {
	raw, err := c.Do(ctx, http.MethodGet, "/api/vm/hdmi", nil)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
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
	_ = json.Unmarshal(raw, &m)
	return m, nil
}
