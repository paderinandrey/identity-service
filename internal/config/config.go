// Package config loads service configuration from environment variables.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment variable names.
const (
	EnvListenAddr      = "LISTEN_ADDR"
	EnvAppEnv          = "APP_ENV"
	EnvLogLevel        = "LOG_LEVEL"
	EnvShutdownTimeout = "SHUTDOWN_TIMEOUT"

	EnvDatabaseURL           = "DATABASE_URL"
	EnvRedisURL              = "REDIS_URL"
	EnvBaseURL               = "BASE_URL"
	EnvFrontendBaseURL       = "FRONTEND_BASE_URL"
	EnvSAMLIdPMetadataURL    = "SAML_IDP_METADATA_URL"
	EnvSAMLAllowIDPInitiated = "SAML_ALLOW_IDP_INITIATED"
	EnvRelayStateSecret      = "RELAY_STATE_SECRET"
	EnvSessionCookieName     = "SESSION_COOKIE_NAME"
	EnvSessionIdleTimeout    = "SESSION_IDLE_TIMEOUT"
	EnvSessionLifetime       = "SESSION_LIFETIME"
	EnvSessionsMaxConcurrent = "SESSIONS_MAX_CONCURRENT"
	EnvUserRevocationDelay   = "USER_REVOCATION_DELAY"
	EnvPermissionsCacheTTL   = "PERMISSIONS_CACHE_TTL"
	EnvCacheMaxEntries       = "CACHE_MAX_ENTRIES"
	EnvSCIMToken             = "SCIM_TOKEN"
	EnvRabbitMQURL           = "RABBITMQ_URL"
	EnvEventsExchange        = "EVENTS_EXCHANGE"
	EnvSentryDSN             = "SENTRY_DSN"
	EnvE2ELoginToken         = "E2E_LOGIN_TOKEN"
)

// Defaults are safe for local development only.
const (
	DefaultListenAddr      = ":8080"
	DefaultAppEnv          = "development"
	DefaultLogLevel        = slog.LevelInfo
	DefaultShutdownTimeout = 10 * time.Second

	DefaultDatabaseURL           = "postgres://identity:identity@localhost:5433/identity_development?sslmode=disable"
	DefaultRedisURL              = "redis://localhost:6380/0"
	DefaultBaseURL               = "http://localhost:8080"
	DefaultFrontendBaseURL       = "http://localhost:8080"
	DefaultRelayStateSecret      = "insecure-development-relay-secret"
	DefaultSessionCookieName     = "__identity_session"
	DefaultSessionIdleTimeout    = 24 * time.Hour
	DefaultSessionLifetime       = 30 * 24 * time.Hour
	DefaultSessionsMaxConcurrent = 100
	DefaultUserRevocationDelay   = 60 * time.Second
	DefaultPermissionsCacheTTL   = 60 * time.Second
	// DefaultCacheMaxEntries bounds each hot-path cache; entries are tens
	// of bytes, so this is megabytes at most.
	DefaultCacheMaxEntries = 10000

	DefaultRabbitMQURL    = "amqp://identity:identity@localhost:5673/"
	DefaultEventsExchange = "identity.events"
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

	DatabaseURL        string
	RedisURL           string
	BaseURL            string
	FrontendBaseURL    string
	SAMLIdPMetadataURL string
	// SAMLAllowIDPInitiated accepts SAML responses that this service did
	// not initiate (no InResponseTo). Off by default: an open product
	// decision, not a security default to relax casually.
	SAMLAllowIDPInitiated bool
	RelayStateSecret      string

	SessionCookieName     string
	SessionIdleTimeout    time.Duration
	SessionLifetime       time.Duration
	SessionsMaxConcurrent int
	UserRevocationDelay   time.Duration
	PermissionsCacheTTL   time.Duration
	// CacheMaxEntries caps the user and permissions caches (each).
	CacheMaxEntries int

	// SCIMToken enables SCIM provisioning endpoints when non-empty.
	SCIMToken string

	RabbitMQURL    string
	EventsExchange string

	// SentryDSN enables error reporting when non-empty.
	SentryDSN string

	// E2ELoginToken enables the programmatic test-login endpoint when
	// non-empty. Must never be set in production.
	E2ELoginToken string
}

// IsDevelopment reports whether the service runs in the development environment.
func (c Config) IsDevelopment() bool { return c.AppEnv == "development" }

// Load reads configuration from the process environment.
// Missing variables fall back to development defaults; outside development
// connection strings, URLs and secrets are required. Invalid values are an error.
func Load() (Config, error) {
	cfg := Config{
		ListenAddr:      DefaultListenAddr,
		AppEnv:          DefaultAppEnv,
		LogLevel:        DefaultLogLevel,
		ShutdownTimeout: DefaultShutdownTimeout,

		SessionCookieName:     DefaultSessionCookieName,
		SessionIdleTimeout:    DefaultSessionIdleTimeout,
		SessionLifetime:       DefaultSessionLifetime,
		SessionsMaxConcurrent: DefaultSessionsMaxConcurrent,
		UserRevocationDelay:   DefaultUserRevocationDelay,
		PermissionsCacheTTL:   DefaultPermissionsCacheTTL,
		CacheMaxEntries:       DefaultCacheMaxEntries,
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

	for _, d := range []struct {
		envVar string
		dst    *time.Duration
	}{
		{EnvShutdownTimeout, &cfg.ShutdownTimeout},
		{EnvSessionIdleTimeout, &cfg.SessionIdleTimeout},
		{EnvSessionLifetime, &cfg.SessionLifetime},
		{EnvUserRevocationDelay, &cfg.UserRevocationDelay},
		{EnvPermissionsCacheTTL, &cfg.PermissionsCacheTTL},
	} {
		if err := loadDuration(d.envVar, d.dst); err != nil {
			return Config{}, err
		}
	}

	if v := os.Getenv(EnvSessionsMaxConcurrent); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return Config{}, fmt.Errorf("%s: want a positive integer, got %q", EnvSessionsMaxConcurrent, v)
		}
		cfg.SessionsMaxConcurrent = n
	}

	if v := os.Getenv(EnvCacheMaxEntries); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return Config{}, fmt.Errorf("%s: want a positive integer, got %q", EnvCacheMaxEntries, v)
		}
		cfg.CacheMaxEntries = n
	}

	if v := os.Getenv(EnvSessionCookieName); v != "" {
		cfg.SessionCookieName = v
	}

	cfg.DatabaseURL = os.Getenv(EnvDatabaseURL)
	cfg.RedisURL = os.Getenv(EnvRedisURL)
	cfg.BaseURL = strings.TrimRight(os.Getenv(EnvBaseURL), "/")
	cfg.FrontendBaseURL = strings.TrimRight(os.Getenv(EnvFrontendBaseURL), "/")
	cfg.SAMLIdPMetadataURL = os.Getenv(EnvSAMLIdPMetadataURL)
	if v := os.Getenv(EnvSAMLAllowIDPInitiated); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("%s: want a boolean, got %q", EnvSAMLAllowIDPInitiated, v)
		}
		cfg.SAMLAllowIDPInitiated = b
	}
	cfg.RelayStateSecret = os.Getenv(EnvRelayStateSecret)

	cfg.RabbitMQURL = os.Getenv(EnvRabbitMQURL)
	cfg.EventsExchange = os.Getenv(EnvEventsExchange)
	if cfg.EventsExchange == "" {
		cfg.EventsExchange = DefaultEventsExchange
	}

	cfg.SentryDSN = os.Getenv(EnvSentryDSN)

	cfg.SCIMToken = os.Getenv(EnvSCIMToken)
	if cfg.SCIMToken != "" && len(cfg.SCIMToken) < 32 {
		return Config{}, fmt.Errorf("%s: token must be at least 32 characters", EnvSCIMToken)
	}

	cfg.E2ELoginToken = os.Getenv(EnvE2ELoginToken)
	if cfg.E2ELoginToken != "" && len(cfg.E2ELoginToken) < 32 {
		return Config{}, fmt.Errorf("%s: token must be at least 32 characters", EnvE2ELoginToken)
	}
	if cfg.E2ELoginToken != "" && cfg.AppEnv == "production" {
		return Config{}, fmt.Errorf("%s must not be set in production", EnvE2ELoginToken)
	}

	if cfg.IsDevelopment() {
		applyDevelopmentDefaults(&cfg)
	} else if missing := missingRequired(cfg); len(missing) > 0 {
		return Config{}, fmt.Errorf("%s=%s requires environment variables: %s", EnvAppEnv, cfg.AppEnv, strings.Join(missing, ", "))
	}

	return cfg, nil
}

func loadDuration(envVar string, dst *time.Duration) error {
	v := os.Getenv(envVar)
	if v == "" {
		return nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fmt.Errorf("%s: invalid duration %q: %w", envVar, v, err)
	}
	if d <= 0 {
		return fmt.Errorf("%s: duration must be positive, got %q", envVar, v)
	}
	*dst = d
	return nil
}

func applyDevelopmentDefaults(cfg *Config) {
	if cfg.DatabaseURL == "" {
		cfg.DatabaseURL = DefaultDatabaseURL
	}
	if cfg.RedisURL == "" {
		cfg.RedisURL = DefaultRedisURL
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.FrontendBaseURL == "" {
		cfg.FrontendBaseURL = DefaultFrontendBaseURL
	}
	if cfg.RelayStateSecret == "" {
		cfg.RelayStateSecret = DefaultRelayStateSecret
	}
	if cfg.RabbitMQURL == "" {
		cfg.RabbitMQURL = DefaultRabbitMQURL
	}
	// SAMLIdPMetadataURL has no development default: without it SSO routes
	// are not mounted and the service logs a warning.
}

func missingRequired(cfg Config) []string {
	var missing []string
	for _, req := range []struct {
		envVar, value string
	}{
		{EnvDatabaseURL, cfg.DatabaseURL},
		{EnvRedisURL, cfg.RedisURL},
		{EnvBaseURL, cfg.BaseURL},
		{EnvFrontendBaseURL, cfg.FrontendBaseURL},
		{EnvSAMLIdPMetadataURL, cfg.SAMLIdPMetadataURL},
		{EnvRelayStateSecret, cfg.RelayStateSecret},
		{EnvRabbitMQURL, cfg.RabbitMQURL},
	} {
		if req.value == "" {
			missing = append(missing, req.envVar)
		}
	}
	return missing
}
