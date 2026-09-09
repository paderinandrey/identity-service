// Command identity-service runs the Identity & Access Service.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/xometry-europe-gmbh/identity-service/internal/config"
	"github.com/xometry-europe-gmbh/identity-service/internal/httpserver"
	"github.com/xometry-europe-gmbh/identity-service/internal/logging"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "identity-service:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := logging.New(os.Stdout, cfg.LogLevel)
	logger.Info("starting identity-service", "env", cfg.AppEnv, "addr", cfg.ListenAddr)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	srv := httpserver.New(cfg.ListenAddr, logger, cfg.ShutdownTimeout)
	if err := srv.Run(ctx); err != nil {
		return err
	}

	logger.Info("stopped")
	return nil
}
