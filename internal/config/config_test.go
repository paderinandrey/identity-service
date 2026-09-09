package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	for _, name := range []string{EnvListenAddr, EnvAppEnv, EnvLogLevel, EnvShutdownTimeout} {
		t.Setenv(name, "")
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ListenAddr != DefaultListenAddr {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, DefaultListenAddr)
	}
	if cfg.AppEnv != DefaultAppEnv {
		t.Errorf("AppEnv = %q, want %q", cfg.AppEnv, DefaultAppEnv)
	}
	if cfg.LogLevel != DefaultLogLevel {
		t.Errorf("LogLevel = %v, want %v", cfg.LogLevel, DefaultLogLevel)
	}
	if cfg.ShutdownTimeout != DefaultShutdownTimeout {
		t.Errorf("ShutdownTimeout = %v, want %v", cfg.ShutdownTimeout, DefaultShutdownTimeout)
	}
}

func TestLoadFromEnv(t *testing.T) {
	t.Setenv(EnvListenAddr, "127.0.0.1:9090")
	t.Setenv(EnvAppEnv, "development")
	t.Setenv(EnvLogLevel, "debug")
	t.Setenv(EnvShutdownTimeout, "30s")
	t.Setenv(EnvSessionIdleTimeout, "1h")
	t.Setenv(EnvSessionsMaxConcurrent, "5")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:9090" {
		t.Errorf("ListenAddr = %q", cfg.ListenAddr)
	}
	if cfg.AppEnv != "development" {
		t.Errorf("AppEnv = %q", cfg.AppEnv)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v", cfg.LogLevel)
	}
	if cfg.ShutdownTimeout != 30*time.Second {
		t.Errorf("ShutdownTimeout = %v", cfg.ShutdownTimeout)
	}
	if cfg.SessionIdleTimeout != time.Hour {
		t.Errorf("SessionIdleTimeout = %v", cfg.SessionIdleTimeout)
	}
	if cfg.SessionsMaxConcurrent != 5 {
		t.Errorf("SessionsMaxConcurrent = %d", cfg.SessionsMaxConcurrent)
	}
}

func TestLoadInvalidValues(t *testing.T) {
	tests := []struct {
		name, envVar, value string
	}{
		{"unknown app env", EnvAppEnv, "qa"},
		{"unknown log level", EnvLogLevel, "verbose"},
		{"malformed shutdown timeout", EnvShutdownTimeout, "soon"},
		{"negative shutdown timeout", EnvShutdownTimeout, "-5s"},
		{"zero shutdown timeout", EnvShutdownTimeout, "0s"},
		{"malformed idle timeout", EnvSessionIdleTimeout, "day"},
		{"negative lifetime", EnvSessionLifetime, "-1h"},
		{"zero revocation delay", EnvUserRevocationDelay, "0s"},
		{"non-numeric sessions limit", EnvSessionsMaxConcurrent, "many"},
		{"zero sessions limit", EnvSessionsMaxConcurrent, "0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.envVar, tt.value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() with %s=%q: want error, got nil", tt.envVar, tt.value)
			}
		})
	}
}

func TestDevelopmentDefaults(t *testing.T) {
	t.Setenv(EnvAppEnv, "development")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DatabaseURL != DefaultDatabaseURL {
		t.Errorf("DatabaseURL = %q, want development default", cfg.DatabaseURL)
	}
	if cfg.RedisURL != DefaultRedisURL {
		t.Errorf("RedisURL = %q, want development default", cfg.RedisURL)
	}
	if cfg.RelayStateSecret != DefaultRelayStateSecret {
		t.Errorf("RelayStateSecret = %q, want development default", cfg.RelayStateSecret)
	}
	if cfg.SAMLIdPMetadataURL != "" {
		t.Errorf("SAMLIdPMetadataURL = %q, want empty in development", cfg.SAMLIdPMetadataURL)
	}
	if cfg.SessionIdleTimeout != DefaultSessionIdleTimeout || cfg.SessionLifetime != DefaultSessionLifetime {
		t.Errorf("session durations = %v/%v, want defaults", cfg.SessionIdleTimeout, cfg.SessionLifetime)
	}
	if cfg.SessionsMaxConcurrent != DefaultSessionsMaxConcurrent {
		t.Errorf("SessionsMaxConcurrent = %d, want default", cfg.SessionsMaxConcurrent)
	}
}

func TestProductionRequiresConnections(t *testing.T) {
	t.Setenv(EnvAppEnv, "production")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() in production without required vars: want error, got nil")
	}
	for _, name := range []string{EnvDatabaseURL, EnvRedisURL, EnvBaseURL, EnvFrontendBaseURL, EnvSAMLIdPMetadataURL, EnvRelayStateSecret} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error must name missing %s, got: %v", name, err)
		}
	}
}

func TestProductionWithAllRequired(t *testing.T) {
	t.Setenv(EnvAppEnv, "production")
	t.Setenv(EnvDatabaseURL, "postgres://u:p@db:5432/identity")
	t.Setenv(EnvRedisURL, "redis://cache:6379/0")
	t.Setenv(EnvBaseURL, "https://id.example.com/")
	t.Setenv(EnvFrontendBaseURL, "https://app.example.com")
	t.Setenv(EnvSAMLIdPMetadataURL, "https://example.okta.com/app/xxx/sso/saml/metadata")
	t.Setenv(EnvRelayStateSecret, "s3cret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.BaseURL != "https://id.example.com" {
		t.Errorf("BaseURL = %q, want trailing slash trimmed", cfg.BaseURL)
	}
}
