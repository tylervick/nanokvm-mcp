package hid

import (
	"bytes"
	"testing"
)

func TestFramePrefixesTheEventByte(t *testing.T) {
	report := []byte{0xAA, 0xBB}
	got := Frame(EventMouse, report)
	if want := []byte{0x02, 0xAA, 0xBB}; !bytes.Equal(got, want) {
		t.Errorf("Frame() = %#v, want %#v", got, want)
	}
	// The frame must own its bytes: callers reuse report buffers.
	got[1] = 0
	if report[0] != 0xAA {
		t.Error("Frame aliased the report it was given")
	}
}

func TestKeyboardReportLayout(t *testing.T) {
	got := KeyboardReport(ModCtrl|ModShift, 0x04, 0x05)
	want := []byte{0x03, 0x00, 0x04, 0x05, 0x00, 0x00, 0x00, 0x00}
	if !bytes.Equal(got, want) {
		t.Errorf("KeyboardReport() = %#v, want %#v", got, want)
	}
	// Byte 1 is reserved and must stay clear; the firmware forwards the report
	// to the host verbatim, and a non-zero byte there is a malformed report.
	if got[1] != 0 {
		t.Errorf("reserved byte = %#x, want 0", got[1])
	}
}

func TestKeyboardReportDropsKeysPastTheSixth(t *testing.T) {
	// The report has room for six. A seventh must be dropped rather than
	// overrun into no byte at all.
	got := KeyboardReport(0, 1, 2, 3, 4, 5, 6, 7, 8)
	want := []byte{0x00, 0x00, 1, 2, 3, 4, 5, 6}
	if !bytes.Equal(got, want) {
		t.Errorf("KeyboardReport() = %#v, want %#v", got, want)
	}
}

func TestKeyboardReleaseIsAllZero(t *testing.T) {
	got := KeyboardRelease()
	if len(got) != 8 {
		t.Fatalf("release report is %d bytes, want 8", len(got))
	}
	for i, b := range got {
		if b != 0 {
			t.Errorf("release report byte %d = %#x, want 0", i, b)
		}
	}
}

func TestAbsoluteMouseReportIsLittleEndian(t *testing.T) {
	// Coordinates whose halves differ, so a byte-order slip cannot pass.
	got := AbsoluteMouseReport(ButtonRight, 0x1234, 0x0567, 0)
	want := []byte{0x02, 0x34, 0x12, 0x67, 0x05, 0x00}
	if !bytes.Equal(got, want) {
		t.Errorf("AbsoluteMouseReport() = %#v, want %#v", got, want)
	}
}

func TestAbsoluteMouseReportClampsToTheAxis(t *testing.T) {
	// The axis is 0..32767; the top bit of a 16-bit field is not part of it, so
	// an out-of-range value must clamp rather than wrap into a nonsense corner.
	if got, want := AbsoluteMouseReport(0, 99999, -5, 0), []byte{0x00, 0xFF, 0x7F, 0x00, 0x00, 0x00}; !bytes.Equal(got, want) {
		t.Errorf("AbsoluteMouseReport() = %#v, want %#v", got, want)
	}
}

func TestRelativeMouseReportEncodesSignedDeltas(t *testing.T) {
	for _, tc := range []struct {
		name          string
		dx, dy, wheel int
		want          []byte
	}{
		{"positive", 10, 20, 3, []byte{0x01, 10, 20, 3}},
		{"negative", -10, -20, -3, []byte{0x01, 0xF6, 0xEC, 0xFD}},
		{"clamped", 5000, -5000, 5000, []byte{0x01, 127, 0x81, 127}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := RelativeMouseReport(ButtonLeft, tc.dx, tc.dy, tc.wheel)
			if !bytes.Equal(got, tc.want) {
				t.Errorf("RelativeMouseReport() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestReportLengthsMatchWhatTheFirmwareAccepts(t *testing.T) {
	// server/service/ws/client.go drops any report that is not exactly these
	// lengths, without an error to the client.
	for _, tc := range []struct {
		name   string
		report []byte
		want   int
	}{
		{"keyboard", KeyboardReport(0, 0x04), 8},
		{"keyboard release", KeyboardRelease(), 8},
		{"absolute mouse", AbsoluteMouseReport(0, 1, 1, 0), 6},
		{"relative mouse", RelativeMouseReport(0, 0, 0, 0), 4},
	} {
		if len(tc.report) != tc.want {
			t.Errorf("%s report is %d bytes, firmware requires %d", tc.name, len(tc.report), tc.want)
		}
	}
}
