package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/rpc"
)

// withCapturedLog installs a debug-level text logger to a buffer as the default
// for the duration of fn, returning what was logged.
func withCapturedLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	fn()
	return buf.String()
}

func TestHandleLogsReceiptAndCompletion(t *testing.T) {
	m := NewMux(api.PluginToDaemon)
	if err := m.Register("workspace.open", noop); err != nil {
		t.Fatalf("Register: %v", err)
	}
	out := withCapturedLog(t, func() {
		if _, err := m.Handle(context.Background(), &rpc.Request{Method: "workspace.open", Params: json.RawMessage("{}")}); err != nil {
			t.Fatalf("Handle: %v", err)
		}
	})
	// A request is logged on receipt (so a blocked call is visible) and on
	// completion, with the method name but never the params.
	if !strings.Contains(out, "rpc recv") || !strings.Contains(out, "rpc done") {
		t.Errorf("missing recv/done log lines:\n%s", out)
	}
	if !strings.Contains(out, "workspace.open") {
		t.Errorf("method name not logged:\n%s", out)
	}
	if !strings.Contains(out, "dur_ms=") {
		t.Errorf("duration not logged:\n%s", out)
	}
}

func TestHandleLogsError(t *testing.T) {
	m := NewMux(api.PluginToDaemon)
	// An unregistered method dispatches to a method-not-found error.
	out := withCapturedLog(t, func() {
		if _, err := m.Handle(context.Background(), &rpc.Request{Method: "workspace.open"}); err == nil {
			t.Fatal("expected an error for an unregistered method")
		}
	})
	if !strings.Contains(out, "rpc error") || !strings.Contains(out, "workspace.open") {
		t.Errorf("error not logged with method:\n%s", out)
	}
}

func TestHandleQuietAboveDebug(t *testing.T) {
	m := NewMux(api.PluginToDaemon)
	if err := m.Register("workspace.open", noop); err != nil {
		t.Fatalf("Register: %v", err)
	}
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	if _, err := m.Handle(context.Background(), &rpc.Request{Method: "workspace.open", Params: json.RawMessage("{}")}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("dispatch logged at info level, want silence:\n%s", buf.String())
	}
}
