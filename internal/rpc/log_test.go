package rpc

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestCallLogsAtDebug confirms a reverse call (Conn.Call) is traced at Debug
// with its method and duration, so a daemon->editor round-trip is visible.
func TestCallLogsAtDebug(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	pairEcho(t) // a.Call(b, "echo", ...)

	out := buf.String()
	if !strings.Contains(out, "rpc call") || !strings.Contains(out, "echo") {
		t.Errorf("reverse call not traced:\n%s", out)
	}
	if !strings.Contains(out, "dur_ms=") {
		t.Errorf("duration not logged:\n%s", out)
	}
}
