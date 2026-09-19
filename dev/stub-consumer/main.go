package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	projection := NewProjection()
	consumer := &Consumer{
		url:        env("RABBITMQ_URL", "amqp://identity:identity@localhost:5673/"),
		exchange:   env("EVENTS_EXCHANGE", "identity.events"),
		queue:      env("QUEUE", "stub-consumer.users"),
		projection: projection,
		logger:     logger,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go consumer.Run(ctx)

	srv := &http.Server{Addr: env("LISTEN_ADDR", ":8080"), Handler: routes(projection), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	logger.Info("stub-consumer listening", "addr", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("http server failed", "error", err)
		os.Exit(1)
	}
}
