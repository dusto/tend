package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/rpc"
)

func noop(context.Context, json.RawMessage) (any, error) { return nil, nil }

func TestRegisterValidation(t *testing.T) {
	m := NewMux(api.PluginToDaemon)

	if err := m.Register("workspace.open", noop); err != nil {
		t.Fatalf("register known method: %v", err)
	}
	if err := m.Register("workspace.open", noop); err == nil {
		t.Fatal("duplicate registration should fail")
	}
	if err := m.Register("event.push", noop); err == nil {
		t.Fatal("wrong-direction method should fail (event.push is daemon->client)")
	}
	if err := m.Register("does.not.exist", noop); err == nil {
		t.Fatal("unknown method should fail")
	}
}

func TestHandleMethodNotFound(t *testing.T) {
	m := NewMux(api.PluginToDaemon)
	_, err := m.Handle(context.Background(), &rpc.Request{Method: "workspace.open"})
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeMethodNotFound {
		t.Fatalf("want method-not-found, got %v", err)
	}
}

func TestTypedHandleAndInvalidParams(t *testing.T) {
	m := NewMux(api.PluginToDaemon)
	if err := Handle(m, "workspace.open", func(_ context.Context, p api.WorkspaceOpenParams) (api.WorkspaceInfo, error) {
		return api.WorkspaceInfo{WorkspaceID: api.WorkspaceID("ws:" + p.Dir)}, nil
	}); err != nil {
		t.Fatalf("typed register: %v", err)
	}

	raw, _ := json.Marshal(api.WorkspaceOpenParams{Dir: "/repo"})
	got, err := m.Handle(context.Background(), &rpc.Request{Method: "workspace.open", Params: raw})
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if info, ok := got.(api.WorkspaceInfo); !ok || info.WorkspaceID != "ws:/repo" {
		t.Fatalf("got %#v", got)
	}

	_, err = m.Handle(context.Background(), &rpc.Request{Method: "workspace.open", Params: json.RawMessage("not json")})
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeInvalidParams {
		t.Fatalf("want invalid-params, got %v", err)
	}
}

func TestEndToEndOverTransport(t *testing.T) {
	m := NewMux(api.PluginToDaemon)
	if err := Handle(m, "workspace.open", func(_ context.Context, p api.WorkspaceOpenParams) (api.WorkspaceInfo, error) {
		return api.WorkspaceInfo{WorkspaceID: api.WorkspaceID("ws:" + p.Dir)}, nil
	}); err != nil {
		t.Fatal(err)
	}

	p1, p2 := net.Pipe()
	server := rpc.NewConn(p1, m)
	client := rpc.NewConn(p2, nil)
	t.Cleanup(func() { _ = server.Close(); _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var info api.WorkspaceInfo
	if err := client.Call(ctx, "workspace.open", api.WorkspaceOpenParams{Dir: "/x"}, &info); err != nil {
		t.Fatalf("call: %v", err)
	}
	if info.WorkspaceID != "ws:/x" {
		t.Fatalf("got %q", info.WorkspaceID)
	}

	// Unregistered method round-trips a method-not-found error.
	err := client.Call(ctx, "workspace.current", nil, nil)
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeMethodNotFound {
		t.Fatalf("want method-not-found over the wire, got %v", err)
	}
}
