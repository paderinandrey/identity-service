// Package observability wires error reporting (Sentry) and Prometheus
// metrics. It is the only package aware of the sentry-go and prometheus
// dependencies; capability packages receive small counter interfaces.
package observability

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Config carries observability settings.
type Config struct {
	SentryDSN   string // empty disables Sentry entirely
	Environment string
	// Transport overrides the Sentry transport (tests).
	Transport sentry.Transport
}

// Observability owns the Sentry client state and the metrics registry.
type Observability struct {
	sentryEnabled bool
	registry      *prometheus.Registry

	httpDuration   *prometheus.HistogramVec
	httpTotal      *prometheus.CounterVec
	outboxPending  prometheus.Gauge
	published      prometheus.Counter
	publishErrors  prometheus.Counter
	signIns        *prometheus.CounterVec
	scimOps        *prometheus.CounterVec
	revocationErr  prometheus.Counter
	cacheRequests  *prometheus.CounterVec
	cacheEntries   *prometheus.GaugeVec
	cacheEvictions *prometheus.CounterVec
}

// Init configures Sentry (when a DSN is set) and builds the metrics
// registry with standard process/Go collectors.
func Init(cfg Config) (*Observability, error) {
	o := &Observability{registry: prometheus.NewRegistry()}

	if cfg.SentryDSN != "" {
		err := sentry.Init(sentry.ClientOptions{
			Dsn:         cfg.SentryDSN,
			Environment: cfg.Environment,
			Transport:   cfg.Transport,
		})
		if err != nil {
			return nil, fmt.Errorf("sentry init: %w", err)
		}
		o.sentryEnabled = true
	}

	o.registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	o.httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request duration by route pattern.",
		Buckets: prometheus.DefBuckets,
	}, []string{"route", "method", "status_class"})
	o.httpTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "HTTP requests by route pattern.",
	}, []string{"route", "method", "status_class"})
	o.outboxPending = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "outbox_pending",
		Help: "User events awaiting publication.",
	})
	o.published = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "events_published_total",
		Help: "User events published to the broker.",
	})
	o.publishErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "event_publish_errors_total",
		Help: "Failed attempts to publish user events.",
	})
	o.signIns = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sign_ins_total",
		Help: "SSO sign-in attempts by result.",
	}, []string{"result"})
	o.scimOps = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "scim_operations_total",
		Help: "SCIM provisioning operations by type.",
	}, []string{"op"})
	o.revocationErr = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "session_revocation_errors_total",
		Help: "Deactivations whose sessions could not be destroyed after the revocation was recorded.",
	})
	o.cacheRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "cache_requests_total",
		Help: "Cache lookups by cache name and result.",
	}, []string{"cache", "result"})
	o.cacheEntries = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "cache_entries",
		Help: "Entries currently held by a lookup cache.",
	}, []string{"cache"})
	o.cacheEvictions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "cache_evictions_total",
		Help: "Entries pushed out of a lookup cache by its capacity.",
	}, []string{"cache"})
	o.registry.MustRegister(o.httpDuration, o.httpTotal, o.outboxPending,
		o.published, o.publishErrors, o.signIns, o.scimOps, o.revocationErr, o.cacheRequests, o.cacheEntries, o.cacheEvictions)

	return o, nil
}

// Shutdown flushes pending Sentry reports.
func (o *Observability) Shutdown() {
	if o.sentryEnabled {
		sentry.Flush(2 * time.Second)
	}
}

// MetricsHandler serves the Prometheus scrape endpoint.
func (o *Observability) MetricsHandler() http.Handler {
	return promhttp.HandlerFor(o.registry, promhttp.HandlerOpts{})
}

// LogHandler tees records: every record goes to base, error-level records
// are additionally reported to Sentry as events.
func (o *Observability) LogHandler(base slog.Handler) slog.Handler {
	if !o.sentryEnabled {
		return base
	}
	return &teeHandler{base: base, sentry: &sentryLogHandler{}}
}

// --- slog → Sentry bridge ---

type teeHandler struct {
	base   slog.Handler
	sentry slog.Handler
}

func (t *teeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return t.base.Enabled(ctx, level) || t.sentry.Enabled(ctx, level)
}

func (t *teeHandler) Handle(ctx context.Context, record slog.Record) error {
	var err error
	if t.base.Enabled(ctx, record.Level) {
		err = t.base.Handle(ctx, record)
	}
	if t.sentry.Enabled(ctx, record.Level) {
		_ = t.sentry.Handle(ctx, record.Clone())
	}
	return err
}

func (t *teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &teeHandler{base: t.base.WithAttrs(attrs), sentry: t.sentry.WithAttrs(attrs)}
}

func (t *teeHandler) WithGroup(name string) slog.Handler {
	return &teeHandler{base: t.base.WithGroup(name), sentry: t.sentry.WithGroup(name)}
}

// sentryLogHandler reports error-level records as Sentry events (issues).
type sentryLogHandler struct {
	attrs []slog.Attr
}

func (h *sentryLogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelError
}

func (h *sentryLogHandler) Handle(_ context.Context, record slog.Record) error {
	event := sentry.NewEvent()
	event.Level = sentry.LevelError
	event.Message = record.Message
	attrs := sentry.Context{}
	for _, attr := range h.attrs {
		attrs[attr.Key] = attr.Value.String()
	}
	record.Attrs(func(attr slog.Attr) bool {
		attrs[attr.Key] = attr.Value.String()
		return true
	})
	event.Contexts["log"] = attrs
	sentry.CaptureEvent(event)
	return nil
}

func (h *sentryLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &sentryLogHandler{attrs: append(append([]slog.Attr{}, h.attrs...), attrs...)}
}

func (h *sentryLogHandler) WithGroup(string) slog.Handler { return h }
