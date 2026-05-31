package daemon

import (
	"context"
	"errors"
	"net"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/handshake"
	"github.com/dusto/tend/internal/rpc"
)

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
		{"commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func newServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "tend.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv, err := New(ln, filepath.Join(dir, "events.log"))
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

// TestShutdownWaitsForConnGoroutines pins the graceful-shutdown contract: once
// Shutdown returns, every per-conn serving goroutine has finished, so no conn
// remains tracked. It guards the invariant, not the specific accept/register
// ordering hazard, which has no observable symptom from outside the package.
func TestShutdownWaitsForConnGoroutines(t *testing.T) {
	srv, path := newServer(t)
	go func() { _ = srv.Serve() }()

	const n = 8
	for i := range n {
		client := dial(t, path)
		// Handshake so the server has accepted and is tracking the conn.
		if _, err := handshake.Do(testCtx(t), client, api.Versions{}); err != nil {
			t.Fatalf("handshake %d: %v", i, err)
		}
	}

	srv.Shutdown()

	if got := srv.connCount(); got != 0 {
		t.Fatalf("after Shutdown: %d conns still tracked, want 0", got)
	}
}

func TestWorkspaceOpenCurrent(t *testing.T) {
	srv, path := newServer(t)
	go func() { _ = srv.Serve() }()
	t.Cleanup(srv.Shutdown)

	repo := initRepo(t)
	client := dial(t, path)

	var opened api.WorkspaceInfo
	if err := client.Call(testCtx(t), "workspace.open", api.WorkspaceOpenParams{Dir: repo}, &opened); err != nil {
		t.Fatalf("workspace.open: %v", err)
	}
	if opened.WorkspaceID == "" || opened.Ephemeral {
		t.Fatalf("open returned %+v", opened)
	}
	if opened.DaemonEpoch != api.DaemonEpoch(srv.Epoch()) {
		t.Errorf("DaemonEpoch = %q, want %q", opened.DaemonEpoch, srv.Epoch())
	}

	var current api.WorkspaceInfo
	if err := client.Call(testCtx(t), "workspace.current", api.WorkspaceCurrentParams{}, &current); err != nil {
		t.Fatalf("workspace.current: %v", err)
	}
	if current != opened {
		t.Errorf("current = %+v, want %+v", current, opened)
	}
}

func TestWorkspaceCurrentBeforeOpen(t *testing.T) {
	srv, path := newServer(t)
	go func() { _ = srv.Serve() }()
	t.Cleanup(srv.Shutdown)

	client := dial(t, path)
	err := client.Call(testCtx(t), "workspace.current", api.WorkspaceCurrentParams{}, nil)
	var rerr *rpc.Error
	if !errors.As(err, &rerr) || rerr.Code != api.ErrNoActiveWorkspace {
		t.Fatalf("current before open = %v, want rpc.Error code %d", err, api.ErrNoActiveWorkspace)
	}
}

// TestWorkspaceCurrentPerConnection verifies each connection has its own active
// workspace: opening on one connection does not change another's current.
func TestWorkspaceCurrentPerConnection(t *testing.T) {
	srv, path := newServer(t)
	go func() { _ = srv.Serve() }()
	t.Cleanup(srv.Shutdown)

	repoA, repoB := initRepo(t), initRepo(t)
	connA, connB := dial(t, path), dial(t, path)

	var a, b api.WorkspaceInfo
	if err := connA.Call(testCtx(t), "workspace.open", api.WorkspaceOpenParams{Dir: repoA}, &a); err != nil {
		t.Fatalf("open A: %v", err)
	}
	if err := connB.Call(testCtx(t), "workspace.open", api.WorkspaceOpenParams{Dir: repoB}, &b); err != nil {
		t.Fatalf("open B: %v", err)
	}
	if a.WorkspaceID == b.WorkspaceID {
		t.Fatal("distinct repos resolved to the same workspace")
	}

	var curA api.WorkspaceInfo
	if err := connA.Call(testCtx(t), "workspace.current", api.WorkspaceCurrentParams{}, &curA); err != nil {
		t.Fatalf("current A: %v", err)
	}
	if curA != a {
		t.Errorf("conn A current = %+v, want its own %+v (not B's)", curA, a)
	}
}

// TestEventsSubscribeWired confirms the event subscription methods are routed by
// the live daemon: a client can subscribe over the socket and gets a Tail back
// (0 for an empty stream).
func TestEventsSubscribeWired(t *testing.T) {
	srv, path := newServer(t)
	go func() { _ = srv.Serve() }()
	t.Cleanup(srv.Shutdown)

	client := dial(t, path)
	var res api.EventsSubscribeResult
	if err := client.Call(testCtx(t), "events.subscribe", api.EventsSubscribeParams{StreamID: "session:x"}, &res); err != nil {
		t.Fatalf("events.subscribe: %v", err)
	}
	if res.Tail != 0 {
		t.Errorf("Tail = %d, want 0 for empty stream", res.Tail)
	}
}

func TestShutdownIdempotent(t *testing.T) {
	srv, _ := newServer(t)
	go func() { _ = srv.Serve() }()
	srv.Shutdown()
	srv.Shutdown() // must not panic or block
}
