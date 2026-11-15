package config

import (
	"strings"
	"testing"
)

func TestLoadRequiresDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("EMAIL_HMAC_SECRET", strings.Repeat("s", 32))

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a missing DATABASE_URL")
	}
}

func TestLoadRequiresLongSecret(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("EMAIL_HMAC_SECRET", "too-short")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a short EMAIL_HMAC_SECRET")
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("EMAIL_HMAC_SECRET", strings.Repeat("s", 32))
	t.Setenv("GRPC_PORT", "")
	t.Setenv("LOG_LEVEL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.GRPCPort != defaultGRPCPort {
		t.Fatalf("GRPCPort = %d, want %d", cfg.GRPCPort, defaultGRPCPort)
	}
	if len(cfg.EmailHMACSecret) != 32 {
		t.Fatalf("secret length = %d, want 32", len(cfg.EmailHMACSecret))
	}
	if cfg.String() == "" {
		t.Fatal("Config.String() returned an empty value")
	}
}

func TestLoadRejectsInvalidPort(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("EMAIL_HMAC_SECRET", strings.Repeat("s", 32))
	t.Setenv("GRPC_PORT", "70000")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted an invalid GRPC_PORT")
	}
}

func TestLoadRejectsInvalidLogLevel(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("EMAIL_HMAC_SECRET", strings.Repeat("s", 32))
	t.Setenv("GRPC_PORT", "")
	t.Setenv("LOG_LEVEL", "verbose")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted an invalid LOG_LEVEL")
	}
}
