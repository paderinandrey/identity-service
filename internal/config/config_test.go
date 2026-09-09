package config

import (
	"log/slog"
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
	t.Setenv(EnvAppEnv, "production")
	t.Setenv(EnvLogLevel, "debug")
	t.Setenv(EnvShutdownTimeout, "30s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:9090" {
		t.Errorf("ListenAddr = %q", cfg.ListenAddr)
	}
	if cfg.AppEnv != "production" {
		t.Errorf("AppEnv = %q", cfg.AppEnv)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v", cfg.LogLevel)
	}
	if cfg.ShutdownTimeout != 30*time.Second {
		t.Errorf("ShutdownTimeout = %v", cfg.ShutdownTimeout)
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
