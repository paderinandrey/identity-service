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

func newTestServer(addr string) *Server {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(addr, logger, 5*time.Second)
}

func TestHealthz(t *testing.T) {
	srv := newTestServer(":0")
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("GET /healthz = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestReadyz(t *testing.T) {
	srv := newTestServer(":0")

	tests := []struct {
		name  string
		ready bool
		want  int
	}{
		{"ready", true, http.StatusOK},
		{"shutting down", false, http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv.SetReady(tt.ready)
			req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			rec := httptest.NewRecorder()

			srv.Handler().ServeHTTP(rec, req)

			if rec.Code != tt.want {
				t.Errorf("GET /readyz = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

// TestRunLifecycle starts a real server on a random port, checks both
// endpoints answer 200, then cancels the context and checks the server
// stops and readiness drops.
func TestRunLifecycle(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}

	srv := newTestServer(addr)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	baseURL := "http://" + addr
	waitFor(t, baseURL+"/healthz", http.StatusOK)
	waitFor(t, baseURL+"/readyz", http.StatusOK)

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop within 5s")
	}

	if srv.ready.Load() {
		t.Error("readiness must be false after shutdown")
	}

	if _, err := http.Get(baseURL + "/healthz"); err == nil {
		t.Error("server must not accept connections after shutdown")
	}
}

func waitFor(t *testing.T, url string, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != want {
				t.Fatalf("GET %s = %d, want %d", url, resp.StatusCode, want)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("GET %s: server did not come up", url)
}
