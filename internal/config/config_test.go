package config

import (
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

// unsetenv removes a variable for the duration of the test. t.Setenv
// records the previous value for restoration; the Unsetenv that follows
// leaves the variable absent rather than empty.
func unsetenv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "")
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDefaults(t *testing.T) {
	for _, name := range []string{EnvListenAddr, EnvAppEnv, EnvLogLevel, EnvShutdownTimeout} {
		unsetenv(t, name)
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

// TestDefaultsMatchTags pins the env-default tags to the exported Default
// constants: README, tests and callers name the constants, cleanenv reads
// the tags, and the two must not drift apart.
func TestDefaultsMatchTags(t *testing.T) {
	for _, name := range []string{
		EnvListenAddr, EnvInternalListenAddr, EnvAppEnv, EnvLogLevel, EnvShutdownTimeout,
		EnvSessionCookieName, EnvSessionIdleTimeout, EnvSessionLifetime, EnvSessionsMaxConcurrent,
		EnvUserRevocationDelay, EnvPermissionsCacheTTL, EnvCacheMaxEntries, EnvEventsExchange, EnvOutboxRetention,
		EnvSAMLAllowIDPInitiated,
	} {
		unsetenv(t, name)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := Config{
		ListenAddr:            DefaultListenAddr,
		InternalListenAddr:    DefaultInternalListenAddr,
		AppEnv:                DefaultAppEnv,
		LogLevel:              DefaultLogLevel,
		ShutdownTimeout:       DefaultShutdownTimeout,
		SessionCookieName:     DefaultSessionCookieName,
		SessionIdleTimeout:    DefaultSessionIdleTimeout,
		SessionLifetime:       DefaultSessionLifetime,
		SessionsMaxConcurrent: DefaultSessionsMaxConcurrent,
		UserRevocationDelay:   DefaultUserRevocationDelay,
		PermissionsCacheTTL:   DefaultPermissionsCacheTTL,
		CacheMaxEntries:       DefaultCacheMaxEntries,
		EventsExchange:        DefaultEventsExchange,
		OutboxRetention:       DefaultOutboxRetention,
	}
	got := Config{
		ListenAddr:            cfg.ListenAddr,
		InternalListenAddr:    cfg.InternalListenAddr,
		AppEnv:                cfg.AppEnv,
		LogLevel:              cfg.LogLevel,
		ShutdownTimeout:       cfg.ShutdownTimeout,
		SessionCookieName:     cfg.SessionCookieName,
		SessionIdleTimeout:    cfg.SessionIdleTimeout,
		SessionLifetime:       cfg.SessionLifetime,
		SessionsMaxConcurrent: cfg.SessionsMaxConcurrent,
		UserRevocationDelay:   cfg.UserRevocationDelay,
		PermissionsCacheTTL:   cfg.PermissionsCacheTTL,
		CacheMaxEntries:       cfg.CacheMaxEntries,
		EventsExchange:        cfg.EventsExchange,
		OutboxRetention:       cfg.OutboxRetention,
	}
	if got != want {
		t.Errorf("defaults from tags = %+v\nwant constants      = %+v", got, want)
	}
	if cfg.SAMLAllowIDPInitiated {
		t.Error("SAMLAllowIDPInitiated default must be false")
	}
}

// Empty values: for text variables the same as absent (the charts and
// shells around the service routinely export empty strings); for typed
// variables an error that names the variable, like any unparsable value.
func TestEmptyValues(t *testing.T) {
	t.Run("text falls back to default", func(t *testing.T) {
		t.Setenv(EnvListenAddr, "")
		t.Setenv(EnvSessionCookieName, "")
		t.Setenv(EnvEventsExchange, "")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.ListenAddr != DefaultListenAddr || cfg.SessionCookieName != DefaultSessionCookieName || cfg.EventsExchange != DefaultEventsExchange {
			t.Errorf("empty text variables must fall back to defaults, got %q %q %q", cfg.ListenAddr, cfg.SessionCookieName, cfg.EventsExchange)
		}
	})
	for _, name := range []string{EnvLogLevel, EnvShutdownTimeout, EnvSessionsMaxConcurrent, EnvSAMLAllowIDPInitiated} {
		t.Run("typed "+name+" rejected", func(t *testing.T) {
			t.Setenv(name, "")
			_, err := Load()
			if err == nil {
				t.Fatalf("Load() with %s=\"\": want error, got nil", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error must name %s, got: %v", name, err)
			}
		})
	}
}

func TestDescribeListsEveryVariable(t *testing.T) {
	text, err := Describe()
	if err != nil {
		t.Fatalf("Describe() error = %v", err)
	}
	for _, name := range []string{EnvListenAddr, EnvDatabaseURL, EnvSCIMToken, EnvE2ELoginToken, EnvOutboxRetention} {
		if !strings.Contains(text, name) {
			t.Errorf("Describe() must mention %s", name)
		}
	}
	if !strings.Contains(text, DefaultSessionCookieName) {
		t.Error("Describe() must show defaults")
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
	for _, name := range []string{EnvDatabaseURL, EnvRedisURL, EnvBaseURL, EnvFrontendBaseURL, EnvSAMLIdPMetadataURL, EnvRelayStateSecret, EnvRabbitMQURL} {
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
	t.Setenv(EnvRelayStateSecret, "a-production-grade-relay-secret-0123456789")
	t.Setenv(EnvRabbitMQURL, "amqp://mq:5672/")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.BaseURL != "https://id.example.com" {
		t.Errorf("BaseURL = %q, want trailing slash trimmed", cfg.BaseURL)
	}
}

func TestSCIMToken(t *testing.T) {
	t.Setenv(EnvAppEnv, "development")

	cfg, err := Load()
	if err != nil || cfg.SCIMToken != "" {
		t.Fatalf("without SCIM_TOKEN: cfg.SCIMToken = %q, err = %v; want empty, nil", cfg.SCIMToken, err)
	}

	t.Setenv(EnvSCIMToken, "short")
	if _, err := Load(); err == nil {
		t.Error("short SCIM token must fail startup")
	}

	long := strings.Repeat("x", 32)
	t.Setenv(EnvSCIMToken, long)
	cfg, err = Load()
	if err != nil || cfg.SCIMToken != long {
		t.Errorf("valid SCIM token: %q, err = %v", cfg.SCIMToken, err)
	}
}

func TestEventsConfig(t *testing.T) {
	t.Setenv(EnvAppEnv, "development")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RabbitMQURL != DefaultRabbitMQURL || cfg.EventsExchange != DefaultEventsExchange {
		t.Errorf("dev defaults: %q %q", cfg.RabbitMQURL, cfg.EventsExchange)
	}

	t.Setenv(EnvEventsExchange, "custom.events")
	cfg, err = Load()
	if err != nil || cfg.EventsExchange != "custom.events" {
		t.Errorf("override: %q, err = %v", cfg.EventsExchange, err)
	}
	if cfg.OutboxRetention != DefaultOutboxRetention {
		t.Errorf("default retention = %v, want %v", cfg.OutboxRetention, DefaultOutboxRetention)
	}

	t.Setenv(EnvOutboxRetention, "48h")
	cfg, err = Load()
	if err != nil || cfg.OutboxRetention != 48*time.Hour {
		t.Errorf("OUTBOX_RETENTION=48h: %v, err = %v", cfg.OutboxRetention, err)
	}
	t.Setenv(EnvOutboxRetention, "-1h")
	if _, err := Load(); err == nil {
		t.Error("non-positive OUTBOX_RETENTION must be rejected")
	}
}

func TestE2ELoginToken(t *testing.T) {
	t.Setenv(EnvAppEnv, "development")

	cfg, err := Load()
	if err != nil || cfg.E2ELoginToken != "" {
		t.Fatalf("without token: %q, err = %v", cfg.E2ELoginToken, err)
	}

	t.Setenv(EnvE2ELoginToken, "short")
	if _, err := Load(); err == nil {
		t.Error("short e2e token must fail startup")
	}

	long := strings.Repeat("e", 32)
	t.Setenv(EnvE2ELoginToken, long)
	cfg, err = Load()
	if err != nil || cfg.E2ELoginToken != long {
		t.Errorf("valid token in development: %q, err = %v", cfg.E2ELoginToken, err)
	}
}

func TestE2ELoginTokenForbiddenInProduction(t *testing.T) {
	t.Setenv(EnvAppEnv, "production")
	t.Setenv(EnvDatabaseURL, "postgres://u:p@db:5432/identity")
	t.Setenv(EnvRedisURL, "redis://cache:6379/0")
	t.Setenv(EnvBaseURL, "https://id.example.com")
	t.Setenv(EnvFrontendBaseURL, "https://app.example.com")
	t.Setenv(EnvSAMLIdPMetadataURL, "https://example.okta.com/metadata")
	t.Setenv(EnvRelayStateSecret, "a-production-grade-relay-secret-0123456789")
	t.Setenv(EnvRabbitMQURL, "amqp://mq:5672/")

	if _, err := Load(); err != nil {
		t.Fatalf("production baseline must load: %v", err)
	}

	t.Setenv(EnvE2ELoginToken, strings.Repeat("e", 32))
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), EnvE2ELoginToken) {
		t.Errorf("production with e2e token: err = %v, want startup error naming the variable", err)
	}

	// Staging is a legitimate place for E2E suites.
	t.Setenv(EnvAppEnv, "staging")
	if _, err := Load(); err != nil {
		t.Errorf("staging with e2e token must load: %v", err)
	}
}

func TestSAMLAllowIDPInitiated(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SAMLAllowIDPInitiated {
		t.Error("IdP-initiated sign-in must be off by default")
	}

	t.Setenv(EnvSAMLAllowIDPInitiated, "true")
	cfg, err = Load()
	if err != nil || !cfg.SAMLAllowIDPInitiated {
		t.Errorf("SAML_ALLOW_IDP_INITIATED=true: cfg=%v err=%v", cfg.SAMLAllowIDPInitiated, err)
	}

	t.Setenv(EnvSAMLAllowIDPInitiated, "maybe")
	if _, err := Load(); err == nil {
		t.Error("non-boolean SAML_ALLOW_IDP_INITIATED must be rejected")
	}
}

func TestCacheMaxEntries(t *testing.T) {
	cfg, err := Load()
	if err != nil || cfg.CacheMaxEntries != DefaultCacheMaxEntries {
		t.Fatalf("default = %d, %v", cfg.CacheMaxEntries, err)
	}
	t.Setenv(EnvCacheMaxEntries, "250")
	cfg, err = Load()
	if err != nil || cfg.CacheMaxEntries != 250 {
		t.Errorf("CACHE_MAX_ENTRIES=250: %d, %v", cfg.CacheMaxEntries, err)
	}
	t.Setenv(EnvCacheMaxEntries, "0")
	if _, err := Load(); err == nil {
		t.Error("non-positive CACHE_MAX_ENTRIES must be rejected")
	}
}

func TestRelayStateSecretStrengthOutsideDevelopment(t *testing.T) {
	setProduction := func(t *testing.T, secret string) {
		t.Helper()
		t.Setenv(EnvAppEnv, "staging")
		t.Setenv(EnvDatabaseURL, "postgres://x")
		t.Setenv(EnvRedisURL, "redis://x")
		t.Setenv(EnvBaseURL, "https://id.example.com")
		t.Setenv(EnvFrontendBaseURL, "https://app.example.com")
		t.Setenv(EnvSAMLIdPMetadataURL, "https://idp.example.com/metadata")
		t.Setenv(EnvRabbitMQURL, "amqp://x")
		t.Setenv(EnvRelayStateSecret, secret)
	}
	setProduction(t, "short")
	if _, err := Load(); err == nil {
		t.Error("a short RelayState secret must be rejected outside development")
	}
	setProduction(t, DefaultRelayStateSecret)
	if _, err := Load(); err == nil {
		t.Error("the development default must be rejected outside development")
	}
	setProduction(t, "a-proper-secret-of-thirty-two-chars-or-more")
	if _, err := Load(); err != nil {
		t.Errorf("a strong secret must pass: %v", err)
	}
}

func TestAppEnvMustBeExplicitInKubernetes(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	unsetenv(t, EnvAppEnv)
	if _, err := Load(); err == nil {
		t.Error("missing APP_ENV inside Kubernetes must be an error, not development")
	}
	t.Setenv(EnvAppEnv, "")
	if _, err := Load(); err == nil {
		t.Error("empty APP_ENV inside Kubernetes must be an error, not development")
	}
	t.Setenv(EnvAppEnv, "development")
	if _, err := Load(); err != nil {
		t.Errorf("explicit development inside Kubernetes must pass: %v", err)
	}
}

func TestInternalListenAddr(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		unsetenv(t, EnvInternalListenAddr)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.InternalListenAddr != DefaultInternalListenAddr {
			t.Errorf("InternalListenAddr = %q, want %q", cfg.InternalListenAddr, DefaultInternalListenAddr)
		}
	})

	t.Run("override", func(t *testing.T) {
		t.Setenv(EnvInternalListenAddr, "127.0.0.1:9091")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.InternalListenAddr != "127.0.0.1:9091" {
			t.Errorf("InternalListenAddr = %q", cfg.InternalListenAddr)
		}
	})

	// The two zones exist to be told apart by port; one address for both
	// would silently merge them, so it is a configuration error with a
	// message naming both variables — not a bind failure later.
	t.Run("same as public", func(t *testing.T) {
		t.Setenv(EnvListenAddr, ":9090")
		t.Setenv(EnvInternalListenAddr, ":9090")
		_, err := Load()
		if err == nil {
			t.Fatal("Load() with equal listen addresses: want error, got nil")
		}
		for _, name := range []string{EnvListenAddr, EnvInternalListenAddr} {
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error must name %s, got: %v", name, err)
			}
		}
	})
}
