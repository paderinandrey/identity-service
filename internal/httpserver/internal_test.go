package httpserver

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func ok(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

func newZonedServer(publicAddr, internalAddr string) *Server {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(publicAddr, logger, 5*time.Second,
		WithRoutes(func(mux *http.ServeMux) { mux.HandleFunc("GET /auth/me", ok) }),
		WithInternal(internalAddr, func(mux *http.ServeMux) { mux.HandleFunc("/internal/session/validate", ok) }),
	)
}

// Each zone knows only its own routes: the internal handler must not be
// reachable through the public listener even by path, and vice versa.
func TestZonesServeDisjointRoutes(t *testing.T) {
	srv := newZonedServer(":0", ":0")

	tests := []struct {
		name    string
		handler http.Handler
		path    string
		want    int
	}{
		{"public: own route", srv.Handler(), "/auth/me", http.StatusOK},
		{"public: healthz", srv.Handler(), "/healthz", http.StatusOK},
		{"public: internal route is absent", srv.Handler(), "/internal/session/validate", http.StatusNotFound},
		{"internal: own route", srv.InternalHandler(), "/internal/session/validate", http.StatusOK},
		{"internal: public route is absent", srv.InternalHandler(), "/auth/me", http.StatusNotFound},
		{"internal: no health endpoints", srv.InternalHandler(), "/healthz", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tt.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != tt.want {
				t.Errorf("GET %s = %d, want %d", tt.path, rec.Code, tt.want)
			}
		})
	}
}

func TestNoInternalZoneByDefault(t *testing.T) {
	if h := newTestServer(":0").InternalHandler(); h != nil {
		t.Error("InternalHandler() must be nil when no internal zone is configured")
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	return freeAddrs(t, 1)[0]
}

// freeAddrs reserves n distinct loopback addresses. All listeners are
// held open until every port is known: closing one before opening the
// next let the kernel hand the same port out twice, so the public and
// internal zones collided on CI and Run failed to bind instead of
// serving.
func freeAddrs(t *testing.T, n int) []string {
	t.Helper()
	listeners := make([]net.Listener, 0, n)
	addrs := make([]string, 0, n)
	for range n {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, ln)
		addrs = append(addrs, ln.Addr().String())
	}
	for _, ln := range listeners {
		if err := ln.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return addrs
}

// startRun runs the server in the background and fails the test at once
// if Run returns before the test expected it to, naming the real error
// instead of leaving a later "server did not come up".
func startRun(ctx context.Context, t *testing.T, srv *Server) chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	return done
}

// waitForRunning is waitFor that also watches the Run result, so a bind
// failure is reported as such.
func waitForRunning(t *testing.T, done chan error, url string, want int) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("Run() returned early: %v", err)
	default:
	}
	waitFor(t, url, want)
}

// Both listeners come up before readiness and go down together on
// shutdown, within the single configured timeout.
func TestRunBothZonesLifecycle(t *testing.T) {
	addrs := freeAddrs(t, 2)
	publicAddr, internalAddr := addrs[0], addrs[1]
	srv := newZonedServer(publicAddr, internalAddr)
	ctx, cancel := context.WithCancel(context.Background())
	done := startRun(ctx, t, srv)

	waitForRunning(t, done, "http://"+publicAddr+"/readyz", http.StatusOK)
	waitForRunning(t, done, "http://"+internalAddr+"/internal/session/validate", http.StatusOK)
	waitForRunning(t, done, "http://"+publicAddr+"/internal/session/validate", http.StatusNotFound)

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop within 5s")
	}
	for _, addr := range []string{publicAddr, internalAddr} {
		if _, err := noKeepAliveClient.Get("http://" + addr + "/"); err == nil {
			t.Errorf("%s must not accept connections after shutdown", addr)
		}
	}
}

// A zone that cannot bind is a startup failure for the whole process:
// the public listener must not stay up alone, or the service would run
// unreachable for ext-auth while reporting itself healthy.
func TestRunFailsWhenInternalAddrBusy(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = busy.Close() }()
	publicAddr := freeAddr(t)

	srv := newZonedServer(publicAddr, busy.Addr().String())
	done := make(chan error, 1)
	go func() { done <- srv.Run(context.Background()) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run() = nil, want bind error for the internal address")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not fail within 5s with a busy internal address")
	}
	if srv.ready.Load() {
		t.Error("readiness must be false after a failed start")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := noKeepAliveClient.Get("http://" + publicAddr + "/healthz"); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("public listener must be closed when the internal zone fails to start")
}

// Shutdown must close both listeners at once: while a slow public
// request drains, the internal listener must already refuse new
// connections instead of admitting ext-auth calls into a shrinking
// deadline (Codex review, PR #11).
func TestShutdownClosesBothListenersConcurrently(t *testing.T) {
	addrs := freeAddrs(t, 2)
	publicAddr, internalAddr := addrs[0], addrs[1]
	entered := make(chan struct{})
	release := make(chan struct{})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(publicAddr, logger, 5*time.Second,
		WithRoutes(func(mux *http.ServeMux) {
			mux.HandleFunc("GET /slow", func(w http.ResponseWriter, _ *http.Request) {
				close(entered)
				<-release
				w.WriteHeader(http.StatusOK)
			})
		}),
		WithInternal(internalAddr, func(mux *http.ServeMux) { mux.HandleFunc("/internal/session/validate", ok) }),
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := startRun(ctx, t, srv)
	waitForRunning(t, done, "http://"+internalAddr+"/internal/session/validate", http.StatusOK)

	slowDone := make(chan error, 1)
	go func() {
		resp, err := noKeepAliveClient.Get("http://" + publicAddr + "/slow")
		if err == nil {
			_ = resp.Body.Close()
		}
		slowDone <- err
	}()
	<-entered
	cancel()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := noKeepAliveClient.Get("http://" + internalAddr + "/internal/session/validate"); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("internal listener still accepts connections while the public zone drains")
		}
		time.Sleep(20 * time.Millisecond)
	}

	close(release)
	if err := <-slowDone; err != nil {
		t.Errorf("in-flight public request must complete: %v", err)
	}
	if err := <-done; err != nil {
		t.Errorf("Run() = %v, want nil", err)
	}
}

// A request that outlives the shutdown timeout is force-closed, and Run
// still returns nil: the stop signal was honoured, so the process must
// exit successfully rather than report the expected timeout as a
// failure (Codex review, PR #11).
func TestShutdownTimeoutIsNotFatal(t *testing.T) {
	publicAddr := freeAddr(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(publicAddr, logger, 200*time.Millisecond,
		WithRoutes(func(mux *http.ServeMux) {
			mux.HandleFunc("GET /stuck", func(_ http.ResponseWriter, _ *http.Request) {
				close(entered)
				<-release
			})
		}),
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	waitFor(t, "http://"+publicAddr+"/healthz", http.StatusOK)
	go func() {
		resp, err := noKeepAliveClient.Get("http://" + publicAddr + "/stuck")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-entered
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run() = %v, want nil after a forced close", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run() did not return after the shutdown timeout")
	}
	close(release)
}

// Readiness is declared only once every zone is bound: a bind failure
// returns before ready ever flips, so /readyz cannot report 200 for a
// process whose ext-auth zone does not exist (Codex review, PR #11).
func TestReadinessWaitsForEveryBind(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = busy.Close() }()
	srv := newZonedServer(freeAddr(t), busy.Addr().String())
	if err := srv.Run(context.Background()); err == nil {
		t.Fatal("Run() = nil, want bind error")
	}
	if srv.ready.Load() {
		t.Error("ready must never be set when a zone failed to bind")
	}
}
