// Package logging configures structured JSON logging for the service.
package logging

import (
	"io"
	"log/slog"
)

// New returns a logger that writes JSON records at or above the given level.
func New(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}
