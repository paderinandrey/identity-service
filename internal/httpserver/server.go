// Package httpserver provides the HTTP server of the service:
// routing, health endpoints and graceful shutdown.
package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"
)

// Server wraps http.Server with readiness state and graceful shutdown.
type Server struct {
	httpServer      *http.Server
	logger          *slog.Logger
	shutdownTimeout time.Duration
	ready           atomic.Bool
}

// New builds a Server listening on addr.
func New(addr string, logger *slog.Logger, shutdownTimeout time.Duration) *Server {
	s := &Server{
		logger:          logger,
		shutdownTimeout: shutdownTimeout,
	}
	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           s.routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	return mux
}

// Handler exposes the router, mainly for tests.
func (s *Server) Handler() http.Handler {
	return s.httpServer.Handler
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeStatus(w, http.StatusOK, `{"status":"ok"}`)
}

func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	if !s.ready.Load() {
		writeStatus(w, http.StatusServiceUnavailable, `{"status":"shutting down"}`)
		return
	}
	writeStatus(w, http.StatusOK, `{"status":"ok"}`)
}

func writeStatus(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body))
}

// SetReady switches the readiness state reported by /readyz.
func (s *Server) SetReady(ready bool) {
	s.ready.Store(ready)
}

// Run serves HTTP until ctx is cancelled, then shuts down gracefully:
// readiness drops first so load balancers stop sending traffic, then
// in-flight requests get up to shutdownTimeout to finish.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	s.ready.Store(true)
	s.logger.Info("http server started", "addr", s.httpServer.Addr)

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	s.ready.Store(false)
	s.logger.Info("shutting down", "timeout", s.shutdownTimeout.String())

	shutdownCtx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
	defer cancel()

	if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
		s.logger.Error("graceful shutdown exceeded timeout, closing connections", "error", err)
		return s.httpServer.Close()
	}
	return nil
}
