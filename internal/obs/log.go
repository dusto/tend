// Package obs holds the daemon's observability wiring. For now that is the
// logging setup: a single text handler to stderr whose level comes from the
// TEND_LOG environment variable. The interactive paths (dispatch, connection
// lifecycle, reverse calls) log at Debug, so the default level keeps the daemon
// quiet while TEND_LOG=debug turns on request tracing for manual verification.
package obs

import (
	"io"
	"log/slog"
	"strings"
)

// EnvLogLevel is the environment variable that sets the daemon's log level.
const EnvLogLevel = "TEND_LOG"

// ParseLevel maps a TEND_LOG value to a slog.Level. It is case-insensitive and
// accepts debug, info, warn (or warning), and error; anything else — including
// the empty string — falls back to Info, so an unset or typo'd value keeps the
// default quiet level rather than erroring.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
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

// NewLogger builds a text logger writing to w at the given level.
func NewLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}
