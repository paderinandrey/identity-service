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
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// Both listeners come up before readiness and go down together on
// shutdown, within the single configured timeout.
func TestRunBothZonesLifecycle(t *testing.T) {
	publicAddr, internalAddr := freeAddr(t), freeAddr(t)
	srv := newZonedServer(publicAddr, internalAddr)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	waitFor(t, "http://"+publicAddr+"/readyz", http.StatusOK)
	waitFor(t, "http://"+internalAddr+"/internal/session/validate", http.StatusOK)
	waitFor(t, "http://"+publicAddr+"/internal/session/validate", http.StatusNotFound)

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
