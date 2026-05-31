package daemon

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/handshake"
	"github.com/dusto/tend/internal/rpc"
)

func newServer(t *testing.T) (*Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tend.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv, err := New(ln)
	if err != nil {
		_ = ln.Close()
		t.Fatalf("New: %v", err)
	}
	return srv, path
}

func dial(t *testing.T, path string) *rpc.Conn {
	t.Helper()
	nc, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c := rpc.NewConn(nc, nil)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestServeHandshake(t *testing.T) {
	srv, path := newServer(t)
	go func() { _ = srv.Serve() }()
	t.Cleanup(srv.Shutdown)

	client := dial(t, path)
	res, err := handshake.Do(testCtx(t), client, api.CurrentVersions())
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if res.DaemonEpoch != api.DaemonEpoch(srv.Epoch()) {
		t.Fatalf("epoch = %q, want %q", res.DaemonEpoch, srv.Epoch())
	}
	if res.Versions != api.CurrentVersions() {
		t.Fatalf("versions = %+v", res.Versions)
	}
}

func TestShutdownStopsServeAndClosesConns(t *testing.T) {
	srv, path := newServer(t)
	served := make(chan error, 1)
	go func() { served <- srv.Serve() }()

	client := dial(t, path)
	// Complete a handshake so the server has accepted and is tracking the conn.
	if _, err := handshake.Do(testCtx(t), client, api.Versions{}); err != nil {
		t.Fatalf("handshake: %v", err)
	}

	srv.Shutdown()

	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after Shutdown")
	}

	select {
	case <-client.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("client conn not closed by Shutdown")
	}
}

func TestShutdownIdempotent(t *testing.T) {
	srv, _ := newServer(t)
	go func() { _ = srv.Serve() }()
	srv.Shutdown()
	srv.Shutdown() // must not panic or block
}
