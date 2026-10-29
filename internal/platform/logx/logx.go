// Package logx builds the process wide structured logger.
package logx

import (
	"log/slog"
	"os"
	"strings"
)

// New returns a JSON slog logger writing to stdout at the requested level.
// Unknown levels fall back to info.
func New(level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		l = slog.LevelDebug
	case "warn", "warning":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l})
	return slog.New(h)
}
