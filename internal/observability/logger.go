// Package observability owns structured logging and Prometheus instrumentation.
package observability

import (
	"io"
	"log/slog"
)

// NewLogger creates a process logger. Production emits JSON for log aggregation;
// local development uses text for readability.
func NewLogger(environment string, level slog.Level, output io.Writer) *slog.Logger {
	options := &slog.HandlerOptions{Level: level}
	if environment == "production" {
		return slog.New(slog.NewJSONHandler(output, options))
	}
	return slog.New(slog.NewTextHandler(output, options))
}
