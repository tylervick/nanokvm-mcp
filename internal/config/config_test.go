package config

import (
	"os"
	"strings"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	os.Clearenv()
	os.Setenv("NANOKVM_HOST", "127.0.0.1")
	os.Setenv("NANOKVM_MCP_TOKEN", "fixed-token")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Username != "admin" || c.Password != "admin" {
		t.Errorf("want admin/admin, got %q/%q", c.Username, c.Password)
	}
	if c.BindAddr != "127.0.0.1:8080" {
		t.Errorf("want default bind 127.0.0.1:8080, got %q", c.BindAddr)
	}
	if c.VerifySSL != true {
		t.Errorf("VerifySSL should default true")
	}
	if c.ReadOnly != false {
		t.Errorf("ReadOnly should default false")
	}
	if c.BearerToken != "fixed-token" {
		t.Errorf("token from env not honored")
	}
}

func TestLoadRequiresHost(t *testing.T) {
	os.Clearenv()
	os.Setenv("NANOKVM_MCP_TOKEN", "x")
	if _, err := Load(); err == nil {
		t.Fatal("expected error when NANOKVM_HOST unset")
	}
}

func TestGenerateTokenUnique(t *testing.T) {
	a, _ := GenerateToken()
	b, _ := GenerateToken()
	if a == b || len(a) < 40 || strings.ContainsAny(a, "+/=") {
		t.Errorf("tokens should be unique, long, url-safe: %q %q", a, b)
	}
}
