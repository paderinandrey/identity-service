// Package config loads service configuration from environment variables.
//
// The Config struct is the single description of the variables: names,
// defaults and help text live in its field tags and are read by cleanenv.
// Everything cleanenv cannot express (cross-field rules, environment-
// dependent requirements, minimum secret lengths) is checked in validate.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/ilyakaznacheev/cleanenv"
)

// Environment variable names.
const (
	EnvListenAddr         = "LISTEN_ADDR"
	EnvInternalListenAddr = "INTERNAL_LISTEN_ADDR"
	EnvAppEnv             = "APP_ENV"
	EnvLogLevel           = "LOG_LEVEL"
	EnvShutdownTimeout    = "SHUTDOWN_TIMEOUT"

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
	EnvOutboxRetention       = "OUTBOX_RETENTION"
	EnvSentryDSN             = "SENTRY_DSN"
	EnvE2ELoginToken         = "E2E_LOGIN_TOKEN"
)

// Defaults are safe for local development only. They repeat the
// env-default tags on Config so that tests and callers can name them;
// TestDefaultsMatchTags keeps the two in step.
const (
	DefaultListenAddr         = ":8080"
	DefaultInternalListenAddr = ":8081"
	DefaultAppEnv             = "development"
	DefaultLogLevel           = slog.LevelInfo
	DefaultShutdownTimeout    = 10 * time.Second

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
	// DefaultOutboxRetention keeps published events for a month: the
	// outbox is the local record of what was delivered and when.
	DefaultOutboxRetention = 30 * 24 * time.Hour
)

var validAppEnvs = map[string]bool{
	"development": true,
	"staging":     true,
	"production":  true,
}

// Config holds the runtime configuration of the service.
//
// Tags: `env` names the variable, `env-default` is the value used when the
// variable is absent, `env-description` is printed by Describe. Connection
// strings, URLs and the RelayState secret carry no env-default on purpose:
// outside development they are required, and in development
// applyDevelopmentDefaults fills them.
type Config struct {
	// ListenAddr serves the public zone (sign-in, provisioning, GraphQL,
	// probes); InternalListenAddr serves the cluster-only zone (session
	// validation for ext-auth, metrics, e2e login). Different ports so
	// the Service and NetworkPolicy can tell them apart.
	ListenAddr         string        `env:"LISTEN_ADDR" env-default:":8080" env-description:"Public zone: sign-in, provisioning, GraphQL, probes"`
	InternalListenAddr string        `env:"INTERNAL_LISTEN_ADDR" env-default:":8081" env-description:"Internal zone: session validation for ext-auth, metrics, e2e login; must differ from LISTEN_ADDR"`
	AppEnv             string        `env:"APP_ENV" env-default:"development" env-description:"One of development, staging, production; must be set explicitly inside Kubernetes"`
	LogLevel           slog.Level    `env:"LOG_LEVEL" env-default:"info" env-description:"One of debug, info, warn, error"`
	ShutdownTimeout    time.Duration `env:"SHUTDOWN_TIMEOUT" env-default:"10s" env-description:"Grace period for in-flight requests on shutdown"`

	DatabaseURL        string `env:"DATABASE_URL" env-description:"PostgreSQL connection string (development: compose DB on localhost:5433)"`
	RedisURL           string `env:"REDIS_URL" env-description:"Redis connection string for sessions (development: localhost:6380)"`
	BaseURL            string `env:"BASE_URL" env-description:"Public base URL of this service (development: http://localhost:8080)"`
	FrontendBaseURL    string `env:"FRONTEND_BASE_URL" env-description:"Where the browser lands after login (development: http://localhost:8080)"`
	SAMLIdPMetadataURL string `env:"SAML_IDP_METADATA_URL" env-description:"Okta IdP metadata URL; SSO routes are not mounted without it"`
	// SAMLAllowIDPInitiated accepts SAML responses that this service did
	// not initiate (no InResponseTo). Off by default: an open product
	// decision, not a security default to relax casually.
	SAMLAllowIDPInitiated bool   `env:"SAML_ALLOW_IDP_INITIATED" env-default:"false" env-description:"Accept SAML responses without InResponseTo (IdP-initiated sign-in)"`
	RelayStateSecret      string `env:"RELAY_STATE_SECRET" env-description:"HMAC secret for the RelayState token; outside development at least 32 characters and not the development default"`

	SessionCookieName     string        `env:"SESSION_COOKIE_NAME" env-default:"__identity_session" env-description:"Session cookie name"`
	SessionIdleTimeout    time.Duration `env:"SESSION_IDLE_TIMEOUT" env-default:"24h" env-description:"Session idle expiry, slides with activity"`
	SessionLifetime       time.Duration `env:"SESSION_LIFETIME" env-default:"720h" env-description:"Absolute session lifetime"`
	SessionsMaxConcurrent int           `env:"SESSIONS_MAX_CONCURRENT" env-default:"100" env-description:"Per-user session cap; oldest sessions are evicted"`
	UserRevocationDelay   time.Duration `env:"USER_REVOCATION_DELAY" env-default:"60s" env-description:"Max staleness of the user active-flag cache"`
	PermissionsCacheTTL   time.Duration `env:"PERMISSIONS_CACHE_TTL" env-default:"60s" env-description:"Max staleness of effective permissions (revocation delay)"`
	// CacheMaxEntries caps the user and permissions caches (each).
	CacheMaxEntries int `env:"CACHE_MAX_ENTRIES" env-default:"10000" env-description:"Capacity of each hot-path cache; least recently used entries are evicted"`

	// SCIMToken enables SCIM provisioning endpoints when non-empty.
	SCIMToken string `env:"SCIM_TOKEN" env-description:"Bearer token for the Okta SCIM client, at least 32 characters; SCIM is disabled without it"`

	RabbitMQURL    string `env:"RABBITMQ_URL" env-description:"RabbitMQ connection string for user events (development: localhost:5673)"`
	EventsExchange string `env:"EVENTS_EXCHANGE" env-default:"identity.events" env-description:"Topic exchange for user-change events"`
	// OutboxRetention is how long published events stay in the outbox.
	OutboxRetention time.Duration `env:"OUTBOX_RETENTION" env-default:"720h" env-description:"How long published events stay in the outbox before being trimmed"`

	// SentryDSN enables error reporting when non-empty.
	SentryDSN string `env:"SENTRY_DSN" env-description:"Error-reporting DSN; Sentry is disabled without it"`

	// E2ELoginToken enables the programmatic test-login endpoint when
	// non-empty. Must never be set in production.
	E2ELoginToken string `env:"E2E_LOGIN_TOKEN" env-description:"Bearer token for programmatic test sessions, at least 32 characters; rejected in production"`
}

// IsDevelopment reports whether the service runs in the development environment.
func (c Config) IsDevelopment() bool { return c.AppEnv == "development" }

// Load reads configuration from the process environment.
//
// Missing variables fall back to development defaults; outside development
// connection strings, URLs and secrets are required. An empty value is the
// same as a missing one for text variables; for numbers, durations and
// booleans it is invalid, like any other unparsable value. Invalid values
// are an error.
func Load() (Config, error) {
	var cfg Config
	if err := cleanenv.ReadEnv(&cfg); err != nil {
		return Config{}, err
	}
	applyTextDefaults(&cfg)
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	cfg.FrontendBaseURL = strings.TrimRight(cfg.FrontendBaseURL, "/")
	if err := validate(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Describe returns the environment variables with their defaults and
// descriptions, as printed by `identity-service env`. It renders the same
// tags cleanenv reads; cleanenv's own GetDescription prints Go kinds
// (int64 for a duration), which is noise for an operator.
func Describe() (string, error) {
	var b strings.Builder
	b.WriteString("identity-service reads its configuration from environment variables.\n")
	t := reflect.TypeOf(Config{})
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, ok := f.Tag.Lookup("env")
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "  %s (%s)\n", name, describeType(f.Type))
		if desc := f.Tag.Get("env-description"); desc != "" {
			fmt.Fprintf(&b, "    \t%s\n", desc)
		}
		if def, ok := f.Tag.Lookup("env-default"); ok {
			fmt.Fprintf(&b, "    \tdefault: %s\n", def)
		} else if def, ok := developmentDefault(name); ok {
			fmt.Fprintf(&b, "    \tdefault (development only): %s\n", def)
		}
		if isRequiredOutsideDevelopment(name) {
			b.WriteString("    \trequired outside development\n")
		}
	}
	return b.String(), nil
}

// developmentDefault names the values applyDevelopmentDefaults fills in.
// They carry no env-default tag on purpose (see Config), so Describe
// renders them here. The RelayState placeholder is described, not
// printed: a literal that must never reach production is not a default
// worth copying.
func developmentDefault(name string) (string, bool) {
	switch name {
	case EnvDatabaseURL:
		return DefaultDatabaseURL, true
	case EnvRedisURL:
		return DefaultRedisURL, true
	case EnvBaseURL:
		return DefaultBaseURL, true
	case EnvFrontendBaseURL:
		return DefaultFrontendBaseURL, true
	case EnvRabbitMQURL:
		return DefaultRabbitMQURL, true
	case EnvRelayStateSecret:
		return "insecure placeholder, rejected outside development", true
	default:
		return "", false
	}
}

func isRequiredOutsideDevelopment(name string) bool {
	switch name {
	case EnvDatabaseURL, EnvRedisURL, EnvBaseURL, EnvFrontendBaseURL,
		EnvSAMLIdPMetadataURL, EnvRelayStateSecret, EnvRabbitMQURL:
		return true
	default:
		return false
	}
}

func describeType(t reflect.Type) string {
	switch {
	case t == reflect.TypeOf(time.Duration(0)):
		return "duration, e.g. 30s or 24h"
	case t == reflect.TypeOf(slog.Level(0)):
		return "log level"
	case t.Kind() == reflect.Bool:
		return "true or false"
	case t.Kind() == reflect.Int:
		return "integer"
	default:
		return "text"
	}
}

// applyTextDefaults treats an empty text variable as absent: cleanenv only
// applies env-default when the variable is missing, while the charts and
// shells around this service routinely export empty strings.
func applyTextDefaults(cfg *Config) {
	v := reflect.ValueOf(cfg).Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		def, ok := f.Tag.Lookup("env-default")
		if !ok || f.Type.Kind() != reflect.String {
			continue
		}
		if fv := v.Field(i); fv.String() == "" {
			fv.SetString(def)
		}
	}
}

// validate holds the rules that tags cannot express.
func validate(cfg *Config) error {
	if cfg.InternalListenAddr == cfg.ListenAddr {
		return fmt.Errorf("%s and %s must differ (got %q for both): the internal zone is told apart by port", EnvListenAddr, EnvInternalListenAddr, cfg.ListenAddr)
	}
	if !validAppEnvs[cfg.AppEnv] {
		return fmt.Errorf("%s: unknown environment %q (want development, staging or production)", EnvAppEnv, cfg.AppEnv)
	}

	for _, d := range []struct {
		envVar string
		value  time.Duration
	}{
		{EnvShutdownTimeout, cfg.ShutdownTimeout},
		{EnvSessionIdleTimeout, cfg.SessionIdleTimeout},
		{EnvSessionLifetime, cfg.SessionLifetime},
		{EnvUserRevocationDelay, cfg.UserRevocationDelay},
		{EnvPermissionsCacheTTL, cfg.PermissionsCacheTTL},
		{EnvOutboxRetention, cfg.OutboxRetention},
	} {
		if d.value <= 0 {
			return fmt.Errorf("%s: duration must be positive, got %q", d.envVar, d.value)
		}
	}
	for _, n := range []struct {
		envVar string
		value  int
	}{
		{EnvSessionsMaxConcurrent, cfg.SessionsMaxConcurrent},
		{EnvCacheMaxEntries, cfg.CacheMaxEntries},
	} {
		if n.value < 1 {
			return fmt.Errorf("%s: want a positive integer, got %d", n.envVar, n.value)
		}
	}

	if cfg.SCIMToken != "" && len(cfg.SCIMToken) < 32 {
		return fmt.Errorf("%s: token must be at least 32 characters", EnvSCIMToken)
	}
	if cfg.E2ELoginToken != "" && len(cfg.E2ELoginToken) < 32 {
		return fmt.Errorf("%s: token must be at least 32 characters", EnvE2ELoginToken)
	}
	if cfg.E2ELoginToken != "" && cfg.AppEnv == "production" {
		return fmt.Errorf("%s must not be set in production", EnvE2ELoginToken)
	}

	// Inside Kubernetes the environment name must be explicit: a pod that
	// forgot APP_ENV must not quietly run with development defaults
	// (insecure cookie, placeholder secrets).
	if os.Getenv(EnvAppEnv) == "" && os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return fmt.Errorf("%s must be set explicitly when running in Kubernetes", EnvAppEnv)
	}

	if cfg.IsDevelopment() {
		applyDevelopmentDefaults(cfg)
		return nil
	}
	if missing := missingRequired(cfg); len(missing) > 0 {
		return fmt.Errorf("%s=%s requires environment variables: %s", EnvAppEnv, cfg.AppEnv, strings.Join(missing, ", "))
	}
	if len(cfg.RelayStateSecret) < 32 {
		return fmt.Errorf("%s must be at least 32 characters outside development", EnvRelayStateSecret)
	}
	if cfg.RelayStateSecret == DefaultRelayStateSecret {
		return fmt.Errorf("%s must not be the development default outside development", EnvRelayStateSecret)
	}
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

func missingRequired(cfg *Config) []string {
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
