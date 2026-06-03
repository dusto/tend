package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/dusto/tend/internal/rpc"
)

// TestMain lets the test binary re-exec itself as a fake ACP process so Spawn
// can be exercised against a real child process. The mode is selected by the
// TEND_FAKE_ACP environment variable.
func TestMain(m *testing.M) {
	switch os.Getenv("TEND_FAKE_ACP") {
	case "respond":
		runFakeAgent()
		return
	case "hang":
		// Never reply to anything; drain stdin until the client closes it.
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}
	os.Exit(m.Run())
}

// runFakeAgent serves the ACP initialize handshake over stdio and exits when the
// client closes the connection.
func runFakeAgent() {
	h := rpc.HandlerFunc(func(_ context.Context, req *rpc.Request) (any, error) {
		if req.Method == "initialize" {
			return InitializeResult{ProtocolVersion: ProtocolVersion}, nil
		}
		return nil, &rpc.Error{Code: rpc.CodeMethodNotFound, Message: "fake: " + req.Method}
	})
	conn := rpc.NewConn(&fakeStdio{}, h)
	<-conn.Done()
}

// fakeStdio is the agent side's transport: read stdin, write stdout.
type fakeStdio struct{}

func (fakeStdio) Read(b []byte) (int, error)  { return os.Stdin.Read(b) }
func (fakeStdio) Write(b []byte) (int, error) { return os.Stdout.Write(b) }
func (fakeStdio) Close() error                { return nil }

func fakeCommand(t *testing.T, mode string) Command {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return Command{Path: exe, Env: append(os.Environ(), "TEND_FAKE_ACP="+mode)}
}

func testCtx(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

func TestSpawnAndInitialize(t *testing.T) {
	c, err := Spawn(fakeCommand(t, "respond"), nil)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	res, err := c.Initialize(testCtx(t, 5*time.Second), InitializeParams{
		ProtocolVersion:    ProtocolVersion,
		ClientCapabilities: ClientCapabilities{FS: FSCapabilities{ReadTextFile: true}},
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if res.ProtocolVersion != ProtocolVersion {
		t.Errorf("ProtocolVersion = %d, want %d", res.ProtocolVersion, ProtocolVersion)
	}

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if c.cmd.ProcessState == nil {
		t.Error("process not reaped after Close")
	}
}

func TestInitializeTimeoutTearsDown(t *testing.T) {
	c, err := Spawn(fakeCommand(t, "hang"), nil)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.Initialize(testCtx(t, 200*time.Millisecond), InitializeParams{ProtocolVersion: ProtocolVersion})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Initialize err = %v, want DeadlineExceeded", err)
	}

	// Failure tears down cleanly: the process is killed and reaped.
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if c.cmd.ProcessState == nil {
		t.Error("hung process not reaped after Close")
	}
}

func TestSpawnBadCommand(t *testing.T) {
	if _, err := Spawn(Command{Path: "/nonexistent/acp-binary-xyz"}, nil); err == nil {
		t.Fatal("Spawn of a missing binary should fail")
	}
}

// peerClient wires a Client to an in-process fake agent (an rpc.Conn with the
// given handler), returning the client and the agent's connection.
func peerClient(t *testing.T, agent rpc.Handler) (*Client, *rpc.Conn) {
	t.Helper()
	a, b := net.Pipe()
	agentConn := rpc.NewConn(a, agent)
	c := newClient(b, nil)
	t.Cleanup(func() { _ = c.Close(); _ = agentConn.Close() })
	return c, agentConn
}

func TestInitializeOverPipe(t *testing.T) {
	c, _ := peerClient(t, rpc.HandlerFunc(func(_ context.Context, req *rpc.Request) (any, error) {
		if req.Method != "initialize" {
			return nil, &rpc.Error{Code: rpc.CodeMethodNotFound}
		}
		return InitializeResult{ProtocolVersion: ProtocolVersion, AuthMethods: json.RawMessage(`["none"]`)}, nil
	}))

	res, err := c.Initialize(testCtx(t, 2*time.Second), InitializeParams{ProtocolVersion: ProtocolVersion})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if string(res.AuthMethods) != `["none"]` {
		t.Errorf("AuthMethods = %s", res.AuthMethods)
	}
}

func TestInitializeAgentError(t *testing.T) {
	c, _ := peerClient(t, rpc.HandlerFunc(func(_ context.Context, _ *rpc.Request) (any, error) {
		return nil, &rpc.Error{Code: -32000, Message: "boom"}
	}))

	_, err := c.Initialize(testCtx(t, 2*time.Second), InitializeParams{ProtocolVersion: ProtocolVersion})
	var rerr *rpc.Error
	if !errors.As(err, &rerr) || rerr.Message != "boom" {
		t.Fatalf("Initialize err = %v, want rpc.Error boom", err)
	}
}

// TestHandlesInboundNotification verifies the client dispatches notifications
// the agent sends (e.g. session/update) to its handler.
func TestHandlesInboundNotification(t *testing.T) {
	got := make(chan string, 1)
	h := rpc.HandlerFunc(func(_ context.Context, req *rpc.Request) (any, error) {
		if req.Notification {
			got <- req.Method
		}
		return nil, nil
	})
	a, b := net.Pipe()
	agentConn := rpc.NewConn(a, nil)
	c := newClient(b, h)
	t.Cleanup(func() { _ = c.Close(); _ = agentConn.Close() })

	if err := agentConn.Notify(testCtx(t, 2*time.Second), "session/update", map[string]string{"k": "v"}); err != nil {
		t.Fatalf("agent Notify: %v", err)
	}
	select {
	case method := <-got:
		if method != "session/update" {
			t.Errorf("notification method = %q", method)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client did not dispatch the agent notification")
	}
}
