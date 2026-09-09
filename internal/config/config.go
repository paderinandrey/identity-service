// Package config loads service configuration from environment variables.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"time"
)

// Environment variable names.
const (
	EnvListenAddr      = "LISTEN_ADDR"
	EnvAppEnv          = "APP_ENV"
	EnvLogLevel        = "LOG_LEVEL"
	EnvShutdownTimeout = "SHUTDOWN_TIMEOUT"
)

// Defaults are safe for local development.
const (
	DefaultListenAddr      = ":8080"
	DefaultAppEnv          = "development"
	DefaultLogLevel        = slog.LevelInfo
	DefaultShutdownTimeout = 10 * time.Second
)

var validAppEnvs = map[string]bool{
	"development": true,
	"staging":     true,
	"production":  true,
}

// Config holds the runtime configuration of the service.
type Config struct {
	ListenAddr      string
	AppEnv          string
	LogLevel        slog.Level
	ShutdownTimeout time.Duration
}

// Load reads configuration from the process environment.
// Missing variables fall back to defaults; invalid values are an error.
func Load() (Config, error) {
	cfg := Config{
		ListenAddr:      DefaultListenAddr,
		AppEnv:          DefaultAppEnv,
		LogLevel:        DefaultLogLevel,
		ShutdownTimeout: DefaultShutdownTimeout,
	}

	if v := os.Getenv(EnvListenAddr); v != "" {
		cfg.ListenAddr = v
	}

	if v := os.Getenv(EnvAppEnv); v != "" {
		if !validAppEnvs[v] {
			return Config{}, fmt.Errorf("%s: unknown environment %q (want development, staging or production)", EnvAppEnv, v)
		}
		cfg.AppEnv = v
	}

	if v := os.Getenv(EnvLogLevel); v != "" {
		var level slog.Level
		if err := level.UnmarshalText([]byte(v)); err != nil {
			return Config{}, fmt.Errorf("%s: unknown log level %q (want debug, info, warn or error)", EnvLogLevel, v)
		}
		cfg.LogLevel = level
	}

	if v := os.Getenv(EnvShutdownTimeout); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("%s: invalid duration %q: %w", EnvShutdownTimeout, v, err)
		}
		if d <= 0 {
			return Config{}, fmt.Errorf("%s: duration must be positive, got %q", EnvShutdownTimeout, v)
		}
		cfg.ShutdownTimeout = d
	}

	return cfg, nil
}
