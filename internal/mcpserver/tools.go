package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tylervick/nanokvm-mcp/internal/backend"
)

// ---- read-only tools ----

type emptyArgs struct{}

type ledOut struct {
	PWR          bool `json:"pwr"`
	HDD          bool `json:"hdd"`
	HDDAvailable bool `json:"hdd_available" jsonschema:"whether this hardware has an HDD LED"`
}

type screenshotArgs struct {
	Width   int `json:"width,omitempty" jsonschema:"max width in px; 0 for backend default"`
	Height  int `json:"height,omitempty" jsonschema:"max height in px; 0 for backend default"`
	Quality int `json:"quality,omitempty" jsonschema:"JPEG quality 1-100; 0 for backend default"`
}

func registerReadOnly(s *mcp.Server, d Deps) {
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(false)}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "nanokvm_screenshot",
		Description: "Capture the target machine's screen as a JPEG image.",
		Annotations: ro,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in screenshotArgs) (*mcp.CallToolResult, any, error) {
		shot, err := d.Backend.Screenshot(ctx, backend.ScreenshotOpts{Width: in.Width, Height: in.Height, Quality: in.Quality})
		if err != nil {
			return nil, nil, err
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.ImageContent{Data: shot.JPEG, MIMEType: "image/jpeg"}},
		}, nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "nanokvm_led_status", Description: "Read the power and HDD LEDs.", Annotations: ro,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, ledOut, error) {
		led, err := d.KVM.LEDStatus(ctx)
		if err != nil {
			return nil, ledOut{}, err
		}
		return nil, ledOut{PWR: led.PWR, HDD: led.HDD, HDDAvailable: led.HDDAvailable}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "nanokvm_hdmi_status", Description: "Get HDMI signal state and resolution.", Annotations: ro,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, map[string]any, error) {
		m, err := d.KVM.HDMIStatus(ctx)
		return nil, m, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "nanokvm_list_images", Description: "List available ISO images on the device.", Annotations: ro,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, map[string]any, error) {
		imgs, err := d.KVM.ListImages(ctx)
		return nil, map[string]any{"images": imgs}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "nanokvm_mounted_image", Description: "Get the currently mounted ISO, if any.", Annotations: ro,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, map[string]any, error) {
		m, err := d.KVM.MountedImage(ctx)
		return nil, m, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "nanokvm_info", Description: "Get NanoKVM device information.", Annotations: ro,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, map[string]any, error) {
		m, err := d.KVM.Info(ctx)
		return nil, m, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "nanokvm_hardware", Description: "Get NanoKVM hardware details.", Annotations: ro,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, map[string]any, error) {
		hw, err := d.KVM.Hardware(ctx)
		if err != nil {
			return nil, nil, err
		}
		return nil, hw.Raw, nil
	})
}

// ---- mutating tools ----

type inputArgs struct {
	Actions []backend.Action `json:"actions" jsonschema:"ordered HID actions; mouse coords normalized to [0,1]"`
}

type powerArgs struct {
	Action string `json:"action" jsonschema:"one of: power, power_long, reset"`
}

type powerCycleArgs struct {
	OffMs int `json:"off_ms,omitempty" jsonschema:"ms to wait between off and on; default 3000"`
}

type mountArgs struct {
	File  string `json:"file" jsonschema:"ISO path on the NanoKVM device"`
	CDROM bool   `json:"cdrom,omitempty" jsonschema:"mount as CD-ROM (default) vs USB disk"`
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func registerMutating(s *mcp.Server, d Deps) {
	destructive := &mcp.ToolAnnotations{DestructiveHint: ptr(true), OpenWorldHint: ptr(false)}
	idempotent := &mcp.ToolAnnotations{DestructiveHint: ptr(false), IdempotentHint: true, OpenWorldHint: ptr(false)}

	rec := func(tool string, args map[string]any, err error) {
		if d.Audit != nil {
			d.Audit.Record(tool, d.Backend.Name(), args, err)
		}
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "nanokvm_input",
		Description: "Send a batch of HID actions (click, move, type, hotkey, scroll, drag, wait). Mouse coordinates are normalized to [0,1] from the top-left. A hotkey combines modifiers (ctrl/shift/alt/meta) with ONE non-modifier key; send multiple hotkey actions for key sequences. wait pauses up to 30000 ms.",
		Annotations: destructive,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in inputArgs) (*mcp.CallToolResult, any, error) {
		err := d.Backend.Input(ctx, in.Actions)
		rec("nanokvm_input", map[string]any{"actions": in.Actions}, err)
		if err != nil {
			return nil, nil, err
		}
		return text(fmt.Sprintf("executed %d actions", len(in.Actions))), nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "nanokvm_power",
		Description: "Press the power or reset button. action=power (short), power_long (force off), reset (no-op on boards without a reset line).",
		Annotations: destructive,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in powerArgs) (*mcp.CallToolResult, any, error) {
		var err error
		switch in.Action {
		case "power":
			err = d.KVM.Power(ctx, "power", 800)
		case "power_long":
			err = d.KVM.Power(ctx, "power", 5000)
		case "reset":
			err = d.KVM.Power(ctx, "reset", 800)
		default:
			err = fmt.Errorf("invalid action %q", in.Action)
		}
		rec("nanokvm_power", map[string]any{"action": in.Action}, err)
		if err != nil {
			return nil, nil, err
		}
		return text("power action sent: " + in.Action), nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "nanokvm_power_cycle", Description: "Force off, wait, then power on. Recommended reset for boards without a reset line.", Annotations: destructive,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in powerCycleArgs) (*mcp.CallToolResult, any, error) {
		off := in.OffMs
		if off == 0 {
			off = 3000
		}
		err := d.KVM.PowerCycle(ctx, off, nil)
		rec("nanokvm_power_cycle", map[string]any{"off_ms": off}, err)
		if err != nil {
			return nil, nil, err
		}
		return text(fmt.Sprintf("power cycled (waited %dms)", off)), nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "nanokvm_mount_iso", Description: "Mount an ISO image to the target as CD-ROM or USB disk.", Annotations: destructive,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in mountArgs) (*mcp.CallToolResult, any, error) {
		err := d.KVM.MountImage(ctx, in.File, in.CDROM)
		rec("nanokvm_mount_iso", map[string]any{"file": in.File, "cdrom": in.CDROM}, err)
		if err != nil {
			return nil, nil, err
		}
		return text("mounted " + in.File), nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "nanokvm_unmount_iso", Description: "Unmount the currently mounted ISO.", Annotations: destructive,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
		err := d.KVM.UnmountImage(ctx)
		rec("nanokvm_unmount_iso", map[string]any{}, err)
		if err != nil {
			return nil, nil, err
		}
		return text("unmounted"), nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "nanokvm_hdmi_reset", Description: "Reset the HDMI capture pipeline (affects capture, not the target).", Annotations: idempotent,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
		err := d.KVM.HDMIReset(ctx)
		rec("nanokvm_hdmi_reset", map[string]any{}, err)
		if err != nil {
			return nil, nil, err
		}
		return text("hdmi reset"), nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "nanokvm_reset_hid", Description: "Reset the USB HID gadget if keyboard/mouse input stops working.", Annotations: idempotent,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
		_, err := d.KVM.Do(ctx, "POST", "/api/hid/reset", nil)
		rec("nanokvm_reset_hid", map[string]any{}, err)
		if err != nil {
			return nil, nil, err
		}
		return text("hid reset"), nil, nil
	})
}
