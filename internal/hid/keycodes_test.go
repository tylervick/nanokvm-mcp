package hid

import "testing"

func TestNamedKeys(t *testing.T) {
	for name, want := range map[string]byte{"enter": 0x28, "a": 0x04, "f1": 0x3A, "tab": 0x2B, "up": 0x52} {
		got, ok := Code(name)
		if !ok || got != want {
			t.Errorf("Code(%q)=%#x,%v want %#x", name, got, ok, want)
		}
	}
	if _, ok := Code("nope"); ok {
		t.Error("unknown key should not resolve")
	}
}

func TestCharCodes(t *testing.T) {
	c, shift, ok := CharCode('A')
	if !ok || c != 0x04 || !shift {
		t.Errorf("CharCode('A')=%#x shift=%v ok=%v", c, shift, ok)
	}
	c, shift, ok = CharCode('a')
	if !ok || c != 0x04 || shift {
		t.Errorf("CharCode('a')=%#x shift=%v ok=%v", c, shift, ok)
	}
}
