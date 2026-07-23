package nanokvm

import (
	"net/url"
	"os/exec"
	"strings"
	"testing"
)

func TestEncryptPasswordRoundTripsWithOpenSSL(t *testing.T) {
	enc, err := EncryptPassword("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	// EncryptPassword URL-encodes its base64 output; undo that for openssl.
	b64, err := url.QueryUnescape(enc)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("openssl", "enc", "-d", "-aes-256-cbc", "-md", "md5",
		"-pass", "pass:nanokvm-sipeed-2024", "-a", "-A")
	cmd.Stdin = strings.NewReader(b64)
	// Deliberately keep stdout and stderr separate: this OpenSSL version
	// (3.x) writes a "deprecated key derivation" warning to stderr for the
	// legacy MD5 KDF, which is required here for NanoKVM wire compatibility
	// and isn't itself an error. CombinedOutput() would fold that warning
	// into the plaintext comparison below and fail a correct round trip.
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("openssl decrypt failed: %v\n%s", err, stderr.String())
	}
	if string(out) != "hunter2" {
		t.Errorf("round trip failed: got %q (stderr: %s)", out, stderr.String())
	}
}

func TestEncryptPasswordSaltIsRandom(t *testing.T) {
	a, _ := EncryptPassword("x")
	b, _ := EncryptPassword("x")
	if a == b {
		t.Error("ciphertext should differ due to random salt")
	}
}
