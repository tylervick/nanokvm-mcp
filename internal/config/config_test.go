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

func TestBoolEnvParsing(t *testing.T) {
	tests := []struct {
		value   string
		want    bool // for NANOKVM_VERIFY_SSL (default true)
		wantErr bool
	}{
		{value: "true", want: true},
		{value: "TRUE", want: true},
		{value: "1", want: true},
		{value: "false", want: false},
		{value: "FALSE", want: false},
		{value: "0", want: false},
		{value: "", want: true},         // empty = unset = keep the (secure) default
		{value: "flase", wantErr: true}, // a typo must not silently disable verification
		{value: "yes", wantErr: true},
	}
	for _, tc := range tests {
		t.Run("value="+tc.value, func(t *testing.T) {
			os.Clearenv()
			os.Setenv("NANOKVM_HOST", "127.0.0.1")
			os.Setenv("NANOKVM_MCP_TOKEN", "x")
			os.Setenv("NANOKVM_VERIFY_SSL", tc.value)
			c, err := Load()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Load() with NANOKVM_VERIFY_SSL=%q should error, got VerifySSL=%v", tc.value, c.VerifySSL)
				}
				if !strings.Contains(err.Error(), "NANOKVM_VERIFY_SSL") {
					t.Errorf("error should name the variable, got: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.VerifySSL != tc.want {
				t.Errorf("NANOKVM_VERIFY_SSL=%q: got %v, want %v", tc.value, c.VerifySSL, tc.want)
			}
		})
	}
}

func TestGenerateTokenUnique(t *testing.T) {
	a, _ := GenerateToken()
	b, _ := GenerateToken()
	if a == b || len(a) < 40 || strings.ContainsAny(a, "+/=") {
		t.Errorf("tokens should be unique, long, url-safe: %q %q", a, b)
	}
}
