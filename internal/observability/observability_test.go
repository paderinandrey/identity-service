package observability

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
)

// captureTransport collects Sentry events instead of sending them.
type captureTransport struct {
	mu     sync.Mutex
	events []*sentry.Event
}

func (t *captureTransport) Configure(sentry.ClientOptions) {}
func (t *captureTransport) SendEvent(event *sentry.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, event)
}
func (t *captureTransport) Flush(_ time.Duration) bool              { return true }
func (t *captureTransport) FlushWithContext(_ context.Context) bool { return true }
func (t *captureTransport) Close()                                  {}

func (t *captureTransport) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.events)
}

func newTestObs(t *testing.T, withSentry bool) (*Observability, *captureTransport) {
	t.Helper()
	transport := &captureTransport{}
	cfg := Config{Environment: "test"}
	if withSentry {
		cfg.SentryDSN = "https://public@example.com/1"
		cfg.Transport = transport
	}
	obs, err := Init(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return obs, transport
}

func TestErrorLogGoesToSentry(t *testing.T) {
	obs, transport := newTestObs(t, true)
	logger := slog.New(obs.LogHandler(slog.NewJSONHandler(io.Discard, nil)))

	logger.Info("just info", "k", "v")
	logger.Error("something failed", "reason", "test")

	if transport.count() != 1 {
		t.Fatalf("sentry events = %d, want 1 (only the error record)", transport.count())
	}
	transport.mu.Lock()
	event := transport.events[0]
	transport.mu.Unlock()
	if event.Message != "something failed" {
		t.Errorf("event message = %q", event.Message)
	}
	attrs, _ := event.Contexts["log"]
	if attrs["reason"] != "test" {
		t.Errorf("event log context = %v", attrs)
	}
}

func TestWithoutDSNIsNoop(t *testing.T) {
	obs, transport := newTestObs(t, false)
	base := slog.NewJSONHandler(io.Discard, nil)
	handler := obs.LogHandler(base)
	if handler != slog.Handler(base) {
		t.Error("without DSN the base handler must be returned unchanged")
	}
	slog.New(handler).Error("boom")
	if transport.count() != 0 {
		t.Error("no events must be sent without DSN")
	}
}

func TestRecoverTurnsPanicInto500(t *testing.T) {
	obs, transport := newTestObs(t, true)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	mux := http.NewServeMux()
	calls := 0
	mux.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) { panic("kaboom") })
	mux.HandleFunc("GET /ok", func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	})
	server := httptest.NewServer(obs.Recover(mux, logger))
	defer server.Close()

	resp, err := http.Get(server.URL + "/boom")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("panicking handler = %d, want 500", resp.StatusCode)
	}

	resp, err = http.Get(server.URL + "/ok")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || calls != 1 {
		t.Error("process must keep serving after a panic")
	}

	if transport.count() == 0 {
		t.Error("panic must be reported to Sentry")
	}
}

func TestHTTPMetricsAndScrape(t *testing.T) {
	obs, _ := newTestObs(t, false)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /widgets/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	server := httptest.NewServer(obs.HTTPMetrics(mux))
	defer server.Close()

	if _, err := http.Get(server.URL + "/widgets/42"); err != nil {
		t.Fatal(err)
	}

	scrape := httptest.NewServer(obs.MetricsHandler())
	defer scrape.Close()
	resp, err := http.Get(scrape.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	text := string(body)

	for _, want := range []string{
		`http_requests_total{method="GET",route="GET /widgets/{id}",status_class="4xx"} 1`,
		"go_goroutines",
		"process_cpu_seconds_total",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("scrape misses %q", want)
		}
	}
}

func TestCounterAdapters(t *testing.T) {
	obs, _ := newTestObs(t, false)

	obs.Relay().Published(3)
	obs.Relay().PublishError()
	obs.Relay().PendingSet(7)
	obs.SignIns().Success()
	obs.SignIns().Failure()
	obs.SCIM().Observe("create")
	obs.Cache("permissions").Observe(true)
	obs.Cache("permissions").Observe(false)

	scrape := httptest.NewServer(obs.MetricsHandler())
	defer scrape.Close()
	resp, err := http.Get(scrape.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	text := string(body)

	for _, want := range []string{
		"events_published_total 3",
		"event_publish_errors_total 1",
		"outbox_pending 7",
		`sign_ins_total{result="success"} 1`,
		`sign_ins_total{result="failure"} 1`,
		`scim_operations_total{op="create"} 1`,
		`cache_requests_total{cache="permissions",result="hit"} 1`,
		`cache_requests_total{cache="permissions",result="miss"} 1`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("scrape misses %q", want)
		}
	}
}
