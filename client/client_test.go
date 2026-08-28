package client_test

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/client"
	"github.com/dusto/tend/internal/dispatch"
	"github.com/dusto/tend/internal/handshake"
	"github.com/dusto/tend/internal/rpc"
)

// fakeDaemon stands up a minimal daemon on a temp unix socket: the version
// handshake, client.register (capturing the params it received), and a
// session.list probe so a test can confirm Call works over the returned Conn.
// It returns the socket path and a channel that yields the register params.
func fakeDaemon(t *testing.T) (string, <-chan api.ClientRegisterParams) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "tend.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	registered := make(chan api.ClientRegisterParams, 1)
	m := dispatch.NewMux(api.PluginToDaemon)
	if err := handshake.Register(m, "epoch-test"); err != nil {
		t.Fatalf("handshake.Register: %v", err)
	}
	if err := dispatch.Handle(m, "client.register",
		func(_ context.Context, p api.ClientRegisterParams) (api.ClientRegisterResult, error) {
			registered <- p
			return api.ClientRegisterResult{}, nil
		}); err != nil {
		t.Fatalf("register handler: %v", err)
	}
	if err := dispatch.Handle(m, "session.list",
		func(_ context.Context, _ api.SessionListParams) (api.SessionListResult, error) {
			return api.SessionListResult{Sessions: []api.SessionInfo{}}, nil
		}); err != nil {
		t.Fatalf("session.list handler: %v", err)
	}
	// On subscribe, push one event back on the caller's connection, so a test can
	// prove daemon->client notifications reach the client's OnNotify hook.
	if err := dispatch.Handle(m, "events.subscribe",
		func(ctx context.Context, _ api.EventsSubscribeParams) (api.EventsSubscribeResult, error) {
			if c := rpc.ConnFromContext(ctx); c != nil {
				_ = c.Notify(context.Background(), "event.push", api.EventPushParams{
					Event: api.Event{Type: "agent_message", Kind: "event", Seq: 1},
				})
			}
			return api.EventsSubscribeResult{}, nil
		}); err != nil {
		t.Fatalf("events.subscribe handler: %v", err)
	}

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			sc := rpc.NewConn(c, m)
			t.Cleanup(func() { _ = sc.Close() })
		}
	}()
	return sock, registered
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestDialRegistersThenCalls(t *testing.T) {
	sock, registered := fakeDaemon(t)

	conn, err := client.Dial(testCtx(t), client.Options{
		Socket:            sock,
		ClientID:          "tend-ui",
		Role:              api.RoleObserver,
		MinPluginToDaemon: "0.8.0",
		PromptCapable:     true,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// The Options mapped onto client.register exactly.
	got := <-registered
	if got.ClientID != "tend-ui" || got.Role != api.RoleObserver || !got.PromptCapable {
		t.Errorf("register params = %+v", got)
	}

	// The returned Conn issues method calls.
	var res api.SessionListResult
	if err := conn.Call(testCtx(t), "session.list", api.SessionListParams{}, &res); err != nil {
		t.Fatalf("session.list: %v", err)
	}
	if res.Sessions == nil {
		t.Error("expected a non-nil (empty) session list")
	}
}

func TestOnNotifyReceivesDaemonPushes(t *testing.T) {
	sock, _ := fakeDaemon(t)
	pushes := make(chan struct {
		method string
		params json.RawMessage
	}, 4)
	conn, err := client.Dial(testCtx(t), client.Options{
		Socket: sock, ClientID: "tend-ui", MinPluginToDaemon: "0.8.0",
		OnNotify: func(method string, params json.RawMessage) {
			pushes <- struct {
				method string
				params json.RawMessage
			}{method, params}
		},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Subscribing makes the fake daemon push one event.push back.
	if err := conn.Call(testCtx(t), "events.subscribe", api.EventsSubscribeParams{}, &api.EventsSubscribeResult{}); err != nil {
		t.Fatalf("events.subscribe: %v", err)
	}

	select {
	case got := <-pushes:
		if got.method != "event.push" {
			t.Fatalf("notify method = %q, want event.push", got.method)
		}
		var p api.EventPushParams
		if err := json.Unmarshal(got.params, &p); err != nil {
			t.Fatalf("decode event.push params: %v", err)
		}
		if p.Event.Type != "agent_message" {
			t.Errorf("event type = %q, want agent_message", p.Event.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the daemon push to reach OnNotify")
	}
}

func TestDialDefaultsToObserver(t *testing.T) {
	sock, registered := fakeDaemon(t)
	conn, err := client.Dial(testCtx(t), client.Options{Socket: sock, ClientID: "tend-cli", MinPluginToDaemon: "0.8.0"})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if got := <-registered; got.Role != api.RoleObserver {
		t.Errorf("role = %q, want observer (zero-value default)", got.Role)
	}
}

func TestDialVersionMismatchFailsAtHandshake(t *testing.T) {
	sock, _ := fakeDaemon(t)
	if _, err := client.Dial(testCtx(t), client.Options{
		Socket: sock, ClientID: "tend-cli", MinPluginToDaemon: "99.0.0",
	}); err == nil {
		t.Fatal("expected a handshake version-mismatch error")
	}
}

func TestDialConnectErrorSurfaces(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.sock")
	if _, err := client.Dial(testCtx(t), client.Options{Socket: missing, ClientID: "tend-cli"}); err == nil {
		t.Fatal("expected a connect error for a missing socket")
	}
}
