// Package config loads sidecar configuration from environment variables.
package config

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

type Config struct {
	Host        string
	Username    string
	Password    string
	UseHTTPS    bool
	VerifySSL   bool
	BindAddr    string
	ReadOnly    bool
	AuditPath   string
	AuditFull   bool
	BearerToken string
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

// boolEnv parses a boolean variable strictly: a typo like "flase" must fail
// loudly at startup rather than silently disable a secure default. Empty is
// treated as unset.
func boolEnv(key string, def bool) (bool, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	switch {
	case strings.EqualFold(v, "true"), v == "1":
		return true, nil
	case strings.EqualFold(v, "false"), v == "0":
		return false, nil
	}
	return def, fmt.Errorf("%s=%q: want true/false/1/0", key, v)
}

// GenerateToken returns 32 random bytes as unpadded base64url.
func GenerateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Load reads configuration from the environment and applies defaults.
func Load() (Config, error) {
	c := Config{
		Host:      os.Getenv("NANOKVM_HOST"),
		Username:  env("NANOKVM_USER", "admin"),
		Password:  env("NANOKVM_PASS", "admin"),
		BindAddr:  env("NANOKVM_MCP_BIND", "127.0.0.1:8080"),
		AuditPath: env("NANOKVM_MCP_AUDIT", "/data/nanokvm-mcp/audit.log"),
	}
	var err error
	for _, b := range []struct {
		dst *bool
		key string
		def bool
	}{
		{&c.UseHTTPS, "NANOKVM_HTTPS", false},
		{&c.VerifySSL, "NANOKVM_VERIFY_SSL", true},
		{&c.ReadOnly, "NANOKVM_MCP_READONLY", false},
		{&c.AuditFull, "NANOKVM_MCP_AUDIT_FULL", false},
	} {
		if *b.dst, err = boolEnv(b.key, b.def); err != nil {
			return Config{}, err
		}
	}
	if c.Host == "" {
		return Config{}, errors.New("NANOKVM_HOST is required")
	}
	c.BearerToken = os.Getenv("NANOKVM_MCP_TOKEN")
	if c.BearerToken == "" {
		t, err := GenerateToken()
		if err != nil {
			return Config{}, err
		}
		c.BearerToken = t
	}
	return c, nil
}
