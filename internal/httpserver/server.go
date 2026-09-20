// Package httpserver provides the HTTP server of the service:
// routing, health endpoints and graceful shutdown.
//
// The server has two zones on different addresses: the public one
// (sign-in, provisioning, GraphQL, probes) and an optional internal one
// (session validation for ext-auth, metrics, e2e login). Each zone has
// its own mux, so an internal route is a 404 on the public listener by
// construction, not by routing convention.
package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Option customizes the server.
type Option func(*Server)

// WithReadyCheck adds a dependency check consulted by /readyz.
func WithReadyCheck(check func(context.Context) error) Option {
	return func(s *Server) { s.readyCheck = check }
}

// WithRoutes registers application routes on the public mux.
func WithRoutes(register func(mux *http.ServeMux)) Option {
	return func(s *Server) { s.registerRoutes = register }
}

// WithInternal enables the internal zone on addr with its own routes.
// The mux starts empty: no health endpoints, nothing shared with the
// public zone.
func WithInternal(addr string, register func(mux *http.ServeMux)) Option {
	return func(s *Server) {
		s.internalAddr = addr
		s.registerInternal = register
	}
}

// WithWrapper wraps each zone's router (middleware such as panic recovery
// and metrics), applied outside route matching.
func WithWrapper(wrap func(http.Handler) http.Handler) Option {
	return func(s *Server) { s.wrapper = wrap }
}

// Server wraps the zone http.Servers with readiness state and graceful shutdown.
type Server struct {
	public           *http.Server
	internal         *http.Server // nil without WithInternal
	logger           *slog.Logger
	shutdownTimeout  time.Duration
	ready            atomic.Bool
	readyCheck       func(context.Context) error
	registerRoutes   func(mux *http.ServeMux)
	internalAddr     string
	registerInternal func(mux *http.ServeMux)
	wrapper          func(http.Handler) http.Handler
}

// New builds a Server whose public zone listens on addr.
func New(addr string, logger *slog.Logger, shutdownTimeout time.Duration, opts ...Option) *Server {
	s := &Server{
		logger:          logger,
		shutdownTimeout: shutdownTimeout,
	}
	for _, opt := range opts {
		opt(s)
	}
	s.public = s.newServer(addr, s.publicRoutes())
	if s.internalAddr != "" {
		s.internal = s.newServer(s.internalAddr, s.internalRoutes())
	}
	return s
}

func (s *Server) newServer(addr string, handler http.Handler) *http.Server {
	if s.wrapper != nil {
		handler = s.wrapper(handler)
	}
	return &http.Server{
		Addr:    addr,
		Handler: handler,
		// Properties of the service, not of an environment: no request
		// body here is larger than 1 MiB, no handler should run longer
		// than this, and idle keep-alives from the proxy are bounded.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

func (s *Server) publicRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	if s.registerRoutes != nil {
		s.registerRoutes(mux)
	}
	return mux
}

func (s *Server) internalRoutes() http.Handler {
	mux := http.NewServeMux()
	if s.registerInternal != nil {
		s.registerInternal(mux)
	}
	return mux
}

// Handler exposes the public router, mainly for tests.
func (s *Server) Handler() http.Handler {
	return s.public.Handler
}

// InternalHandler exposes the internal router, mainly for tests; nil
// when no internal zone is configured.
func (s *Server) InternalHandler() http.Handler {
	if s.internal == nil {
		return nil
	}
	return s.internal.Handler
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeStatus(w, http.StatusOK, `{"status":"ok"}`)
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if !s.ready.Load() {
		writeStatus(w, http.StatusServiceUnavailable, `{"status":"shutting down"}`)
		return
	}
	if s.readyCheck != nil {
		if err := s.readyCheck(r.Context()); err != nil {
			s.logger.Warn("readiness check failed", "error", err)
			writeStatus(w, http.StatusServiceUnavailable, `{"status":"dependency unavailable"}`)
			return
		}
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

func (s *Server) servers() []*http.Server {
	if s.internal == nil {
		return []*http.Server{s.public}
	}
	return []*http.Server{s.public, s.internal}
}

// Run serves both zones until ctx is cancelled, then shuts down
// gracefully: readiness drops first so load balancers stop sending
// traffic, then in-flight requests of every zone get up to
// shutdownTimeout to finish. Both addresses are bound before readiness
// is declared, so /readyz never reports 200 while a zone is still
// unbound; a zone that cannot bind fails Run as a whole and no listener
// stays up alone. A request that outlives the shutdown timeout is
// force-closed and logged, not turned into a failed exit: the signal
// was honoured (Codex review, PR #11).
func (s *Server) Run(ctx context.Context) error {
	servers := s.servers()
	listeners := make([]net.Listener, 0, len(servers))
	for _, srv := range servers {
		ln, err := net.Listen("tcp", srv.Addr)
		if err != nil {
			for _, bound := range listeners {
				_ = bound.Close()
			}
			return fmt.Errorf("listen %s: %w", srv.Addr, err)
		}
		listeners = append(listeners, ln)
	}

	errCh := make(chan error, len(servers))
	for i, srv := range servers {
		go func() {
			err := srv.Serve(listeners[i])
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
				return
			}
			errCh <- nil
		}()
	}

	s.ready.Store(true)
	for _, srv := range servers {
		s.logger.Info("http server started", "addr", srv.Addr)
	}

	var runErr error
	select {
	case runErr = <-errCh:
		if runErr == nil {
			// Serve returned ErrServerClosed without our shutdown: nothing
			// to wait for, but treat it like a cancellation.
			runErr = errors.New("http server stopped unexpectedly")
		}
	case <-ctx.Done():
	}

	s.ready.Store(false)
	s.logger.Info("shutting down", "timeout", s.shutdownTimeout.String())

	shutdownCtx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
	defer cancel()

	// Both zones shut down at once: each Shutdown closes its listener
	// immediately and then drains, so neither zone keeps admitting
	// requests while the other finishes its in-flight ones.
	closeErrs := make([]error, len(servers))
	var wg sync.WaitGroup
	for i, srv := range servers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := srv.Shutdown(shutdownCtx); err != nil {
				s.logger.Error("graceful shutdown exceeded timeout, closing connections", "addr", srv.Addr, "error", err)
				closeErrs[i] = srv.Close()
			}
		}()
	}
	wg.Wait()
	if runErr != nil {
		return runErr
	}
	return errors.Join(closeErrs...)
}
