// Package config loads sidecar configuration from environment variables.
package config

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
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

func boolEnv(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	return strings.EqualFold(v, "true") || v == "1"
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
		UseHTTPS:  boolEnv("NANOKVM_HTTPS", false),
		VerifySSL: boolEnv("NANOKVM_VERIFY_SSL", true),
		BindAddr:  env("NANOKVM_MCP_BIND", "127.0.0.1:8080"),
		ReadOnly:  boolEnv("NANOKVM_MCP_READONLY", false),
		AuditPath: env("NANOKVM_MCP_AUDIT", "/data/nanokvm-mcp/audit.log"),
		AuditFull: boolEnv("NANOKVM_MCP_AUDIT_FULL", false),
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
