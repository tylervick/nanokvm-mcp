// report.go builds the USB HID input reports carried by NanoKVM's /api/ws.
//
// The framing, the report layouts, and the modifier/button bit assignments are
// dictated by the upstream GPL-3.0 project github.com/sipeed/NanoKVM: see
// server/service/ws/client.go, which switches on the first byte of each binary
// frame and validates the report length, and web/src/lib/{keyboard,mouse}.ts,
// which build the same reports in the browser. This is a clean-room Go
// implementation of that wire format and is licensed GPL-3.0 accordingly.

package hid

import "encoding/binary"

// Websocket event types. The firmware switches on the first byte of every
// frame; a frame that starts with anything else is discarded without a reply,
// so an encoder that gets this wrong looks like success from the client side.
const (
	EventHeartbeat byte = 0
	EventKeyboard  byte = 1
	EventMouse     byte = 2
)

// Keyboard modifier bits, packed into byte 0 of the keyboard report. These are
// the left-hand modifiers; the right-hand ones occupy bits 4..7.
const (
	ModCtrl  byte = 1 << 0
	ModShift byte = 1 << 1
	ModAlt   byte = 1 << 2
	ModMeta  byte = 1 << 3
)

// Mouse button bits, packed into byte 0 of either mouse report. This is a
// bitmask, not an index: a zero byte means no button is held.
const (
	ButtonLeft   byte = 1 << 0
	ButtonRight  byte = 1 << 1
	ButtonMiddle byte = 1 << 2
)

// MaxCoord is the top of the absolute mouse axis, which spans 0..32767.
const MaxCoord = 0x7FFF

// maxKeys is the number of simultaneously-held keys an 8-byte report can carry.
const maxKeys = 6

// Frame prefixes a report with its event byte, producing a complete /api/ws
// message. It must go out as a binary frame: JSON text would arrive with '['
// (91) in the event position and match nothing.
func Frame(event byte, report []byte) []byte {
	frame := make([]byte, 0, len(report)+1)
	frame = append(frame, event)
	return append(frame, report...)
}

// KeyboardReport builds the 8-byte report: a modifier bitmap, a reserved byte,
// then up to six held usage codes. Keys past the sixth are dropped, as they are
// in the browser client.
func KeyboardReport(mod byte, keys ...byte) []byte {
	report := make([]byte, 8)
	report[0] = mod
	if len(keys) > maxKeys {
		keys = keys[:maxKeys]
	}
	copy(report[2:], keys)
	return report
}

// KeyboardRelease builds the report that lifts every key and modifier.
func KeyboardRelease() []byte { return make([]byte, 8) }

// AbsoluteMouseReport builds the 6-byte report for the tablet device: a button
// bitmap, x and y as little-endian 16-bit coordinates, and a wheel delta.
// Because the position travels with the buttons, a press, a drag and a release
// each state where they happen — there is no implicit cursor to lose track of.
func AbsoluteMouseReport(buttons byte, x, y, wheel int) []byte {
	report := make([]byte, 6)
	report[0] = buttons
	binary.LittleEndian.PutUint16(report[1:3], clampCoord(x))
	binary.LittleEndian.PutUint16(report[3:5], clampCoord(y))
	report[5] = clampDelta(wheel)
	return report
}

// RelativeMouseReport builds the 4-byte report for the relative device: a
// button bitmap and signed dx/dy/wheel deltas. Used where no position is
// implied — pressing at wherever the cursor already is, or scrolling.
func RelativeMouseReport(buttons byte, dx, dy, wheel int) []byte {
	return []byte{buttons, clampDelta(dx), clampDelta(dy), clampDelta(wheel)}
}

func clampCoord(v int) uint16 {
	if v < 0 {
		return 0
	}
	if v > MaxCoord {
		return MaxCoord
	}
	return uint16(v)
}

// clampDelta bounds a signed delta to what one report byte carries and returns
// it in two's complement, which is how the HID descriptor reads it.
func clampDelta(v int) byte {
	if v < -127 {
		v = -127
	}
	if v > 127 {
		v = 127
	}
	return byte(v & 0xFF)
}
