package observability

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/getsentry/sentry-go"
)

// statusRecorder captures the response status for metrics.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Recover turns handler panics into 500 responses, keeps the process
// serving and reports the panic to Sentry when enabled.
func (o *Observability) Recover(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(rec) // deliberate connection abort, not a failure
			}
			logger.Error("panic in http handler", "panic", rec, "path", r.URL.Path)
			if o.sentryEnabled {
				hub := sentry.CurrentHub().Clone()
				hub.RecoverWithContext(r.Context(), rec)
			}
			// Best effort: headers may already be written.
			http.Error(w, "internal error", http.StatusInternalServerError)
		}()
		next.ServeHTTP(w, r)
	})
}

// HTTPMetrics records request duration and counts by route pattern.
func (o *Observability) HTTPMetrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)

		route := r.Pattern // set by ServeMux during next.ServeHTTP
		if route == "" {
			route = "unmatched"
		}
		class := strconv.Itoa(recorder.status/100) + "xx"
		o.httpDuration.WithLabelValues(route, r.Method, class).Observe(time.Since(started).Seconds())
		o.httpTotal.WithLabelValues(route, r.Method, class).Inc()
	})
}

// --- counter adapters consumed by capability packages ---

// RelayMetrics instruments the outbox publisher.
type RelayMetrics struct{ o *Observability }

// Relay returns the outbox publisher metrics adapter.
func (o *Observability) Relay() *RelayMetrics { return &RelayMetrics{o} }

// Published counts successfully published events.
func (m *RelayMetrics) Published(n int) { m.o.published.Add(float64(n)) }

// PublishError counts failed publish attempts.
func (m *RelayMetrics) PublishError() { m.o.publishErrors.Inc() }

// PendingSet reports the current number of unpublished events.
func (m *RelayMetrics) PendingSet(n int) { m.o.outboxPending.Set(float64(n)) }

// SignInMetrics instruments SSO sign-ins.
type SignInMetrics struct{ o *Observability }

// SignIns returns the SSO metrics adapter.
func (o *Observability) SignIns() *SignInMetrics { return &SignInMetrics{o} }

// Success counts a completed sign-in.
func (m *SignInMetrics) Success() { m.o.signIns.WithLabelValues("success").Inc() }

// Failure counts a rejected sign-in.
func (m *SignInMetrics) Failure() { m.o.signIns.WithLabelValues("failure").Inc() }

// SCIMMetrics instruments provisioning operations.
type SCIMMetrics struct{ o *Observability }

// SCIM returns the provisioning metrics adapter.
func (o *Observability) SCIM() *SCIMMetrics { return &SCIMMetrics{o} }

// Observe counts one provisioning operation of the given type.
func (m *SCIMMetrics) Observe(op string) { m.o.scimOps.WithLabelValues(op).Inc() }

// RevocationError implements scim.OpMetrics.
func (m *SCIMMetrics) RevocationError() { m.o.revocationErr.Inc() }

// CacheMetrics instruments a named lookup cache.
type CacheMetrics struct {
	o    *Observability
	name string
}

// Cache returns a hit/miss adapter for the named cache.
func (o *Observability) Cache(name string) *CacheMetrics {
	return &CacheMetrics{o: o, name: name}
}

// Observe counts one cache lookup.
func (m *CacheMetrics) Observe(hit bool) {
	result := "miss"
	if hit {
		result = "hit"
	}
	m.o.cacheRequests.WithLabelValues(m.name, result).Inc()
}
