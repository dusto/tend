package obs

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug":    slog.LevelDebug,
		"DEBUG":    slog.LevelDebug,
		"info":     slog.LevelInfo,
		"warn":     slog.LevelWarn,
		"warning":  slog.LevelWarn,
		"error":    slog.LevelError,
		"":         slog.LevelInfo, // unset -> quiet default
		"nonsense": slog.LevelInfo, // typo -> quiet default, not an error
	}
	for in, want := range cases {
		if got := ParseLevel(in); got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}
	// Surrounding whitespace is trimmed.
	if got := ParseLevel("  debug  "); got != slog.LevelDebug {
		t.Errorf("ParseLevel with surrounding whitespace = %v, want debug", got)
	}
}

func TestNewLoggerRespectsLevel(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, slog.LevelInfo)
	log.Debug("hidden")
	log.Info("shown")
	out := buf.String()
	if strings.Contains(out, "hidden") {
		t.Errorf("debug record leaked at info level: %q", out)
	}
	if !strings.Contains(out, "shown") {
		t.Errorf("info record missing: %q", out)
	}
}
