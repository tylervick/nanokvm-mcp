// Package hid holds USB HID keyboard usage codes.
//
// Codes are taken from the USB HID Usage Tables (HUT) spec, section 10
// (Keyboard/Keypad Page 0x07): https://usb.org/sites/default/files/hut1_22.pdf
// These are published facts, not code copied from any implementation.
package hid

var named = map[string]byte{
	"a": 0x04, "b": 0x05, "c": 0x06, "d": 0x07, "e": 0x08, "f": 0x09, "g": 0x0A,
	"h": 0x0B, "i": 0x0C, "j": 0x0D, "k": 0x0E, "l": 0x0F, "m": 0x10, "n": 0x11,
	"o": 0x12, "p": 0x13, "q": 0x14, "r": 0x15, "s": 0x16, "t": 0x17, "u": 0x18,
	"v": 0x19, "w": 0x1A, "x": 0x1B, "y": 0x1C, "z": 0x1D,
	"1": 0x1E, "2": 0x1F, "3": 0x20, "4": 0x21, "5": 0x22, "6": 0x23, "7": 0x24,
	"8": 0x25, "9": 0x26, "0": 0x27,
	"enter": 0x28, "return": 0x28, "escape": 0x29, "esc": 0x29, "backspace": 0x2A,
	"tab": 0x2B, "space": 0x2C, "minus": 0x2D, "equal": 0x2E,
	"f1": 0x3A, "f2": 0x3B, "f3": 0x3C, "f4": 0x3D, "f5": 0x3E, "f6": 0x3F,
	"f7": 0x40, "f8": 0x41, "f9": 0x42, "f10": 0x43, "f11": 0x44, "f12": 0x45,
	"insert": 0x49, "home": 0x4A, "pageup": 0x4B, "delete": 0x4C, "end": 0x4D,
	"pagedown": 0x4E, "right": 0x4F, "left": 0x50, "down": 0x51, "up": 0x52,
}

func Code(name string) (byte, bool) {
	c, ok := named[name]
	return c, ok
}

var shifted = map[rune]byte{
	'!': 0x1E, '@': 0x1F, '#': 0x20, '$': 0x21, '%': 0x22, '^': 0x23, '&': 0x24,
	'*': 0x25, '(': 0x26, ')': 0x27, '_': 0x2D, '+': 0x2E,
}

// CharCode returns the usage code and shift requirement for a printable rune.
func CharCode(r rune) (code byte, shift bool, ok bool) {
	if r >= 'a' && r <= 'z' {
		c, _ := Code(string(r))
		return c, false, true
	}
	if r >= 'A' && r <= 'Z' {
		c, _ := Code(string(r + 32))
		return c, true, true
	}
	if r >= '0' && r <= '9' {
		c, _ := Code(string(r))
		return c, false, true
	}
	if c, ok := shifted[r]; ok {
		return c, true, true
	}
	if r == ' ' {
		return 0x2C, false, true
	}
	return 0, false, false
}
