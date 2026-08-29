package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/client"
	"github.com/dusto/tend/client/clienttest"
)

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return c
}

// A daemon error reaches the caller as *client.Error with the right code, so
// external code can branch on it (the reason for exposing codes: tend-du1.13).
func TestCallSurfacesDaemonErrorCode(t *testing.T) {
	srv := clienttest.New(t)
	srv.Handle("events.subscribe", func(json.RawMessage) (any, error) {
		return nil, clienttest.ErrorCode(api.ErrCursorCompacted, "cursor predates retention")
	})

	conn, err := client.Dial(ctx(t), client.Options{Socket: srv.Socket(), ClientID: "test", MinPluginToDaemon: "0.8.0"})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	callErr := conn.Call(ctx(t), "events.subscribe", api.EventsSubscribeParams{}, &api.EventsSubscribeResult{})
	if callErr == nil {
		t.Fatal("expected an error")
	}
	if !client.IsCode(callErr, api.ErrCursorCompacted) {
		t.Errorf("IsCode(ErrCursorCompacted) = false for %v", callErr)
	}
	if e, ok := client.AsError(callErr); !ok || e.Code != api.ErrCursorCompacted {
		t.Errorf("AsError = %v, %v", e, ok)
	}
	if client.IsCode(callErr, api.ErrConflict) {
		t.Error("IsCode should not match a different code")
	}
}

// A successful call returns nil; a non-daemon error (e.g. a closed connection)
// is not misreported as a *client.Error.
func TestCallOKAndNonDaemonError(t *testing.T) {
	srv := clienttest.New(t)
	srv.Handle("session.list", func(json.RawMessage) (any, error) {
		return api.SessionListResult{Sessions: []api.SessionInfo{{SessionID: "s1"}}}, nil
	})
	conn, err := client.Dial(ctx(t), client.Options{Socket: srv.Socket(), ClientID: "test", MinPluginToDaemon: "0.8.0"})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	var res api.SessionListResult
	if err := conn.Call(ctx(t), "session.list", api.SessionListParams{}, &res); err != nil {
		t.Fatalf("session.list: %v", err)
	}
	if len(res.Sessions) != 1 {
		t.Fatalf("sessions = %+v", res.Sessions)
	}

	_ = conn.Close()
	err = conn.Call(ctx(t), "session.list", api.SessionListParams{}, &res)
	if err == nil {
		t.Fatal("expected an error after Close")
	}
	var ce *client.Error
	if errors.As(err, &ce) {
		t.Errorf("a transport error should not be a *client.Error: %v", err)
	}
}
