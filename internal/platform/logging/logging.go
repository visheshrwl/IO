// Package logging builds the structured logger shared by every service in
// this repo. Logs are JSON on stderr so a collector (Loki, ELK, Cloud
// Logging) can parse them without a regex, and every line carries the
// service name and version for filtering across a fleet.
package logging

import (
	"log/slog"
	"os"
	"strings"
)

// New returns a JSON logger annotated with the service name and version.
// level is one of "debug", "info", "warn", "error"; anything else falls back
// to "info". The returned logger is also installed as slog.Default so code
// that logs via the package-level slog functions gets the same handler.
func New(service, version, level string) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: parseLevel(level),
	})
	logger := slog.New(handler).With(
		slog.String("service", service),
		slog.String("version", version),
	)
	slog.SetDefault(logger)
	return logger
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
