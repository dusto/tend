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
	case "codex":
		runFakeCodex()
		return
	}
	os.Exit(m.Run())
}

// runFakeCodex is a fuller fake ACP agent: it serves initialize and session/new,
// and on session/prompt streams a few session/update notifications before
// returning end_turn. It models the milestone-0 Codex turn shape.
func runFakeCodex() {
	h := rpc.HandlerFunc(func(ctx context.Context, req *rpc.Request) (any, error) {
		switch req.Method {
		case "initialize":
			return InitializeResult{ProtocolVersion: ProtocolVersion}, nil
		case MethodNewSession:
			return NewSessionResult{SessionID: "sess-1"}, nil
		case MethodPrompt:
			var p PromptParams
			_ = json.Unmarshal(req.Params, &p)
			conn := rpc.ConnFromContext(ctx)
			send := func(body map[string]any) {
				_ = conn.Notify(context.Background(), SessionUpdateMethod, map[string]any{
					"sessionId": p.SessionID, "update": body,
				})
			}
			send(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"text": "hello "}})
			send(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"text": "world"}})
			send(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "t1", "title": "read_file"})
			send(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t1", "status": "completed"})
			return PromptResult{StopReason: "end_turn"}, nil
		}
		return nil, &rpc.Error{Code: rpc.CodeMethodNotFound, Message: "fake codex: " + req.Method}
	})
	conn := rpc.NewConn(&fakeStdio{}, h)
	<-conn.Done()
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
	c, res, err := SpawnAndInitialize(testCtx(t, 5*time.Second), fakeCommand(t, "respond"),
		InitializeParams{
			ProtocolVersion:    ProtocolVersion,
			ClientCapabilities: ClientCapabilities{FS: FSCapabilities{ReadTextFile: true}},
		}, nil)
	if err != nil {
		t.Fatalf("SpawnAndInitialize: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if res.ProtocolVersion != ProtocolVersion {
		t.Errorf("ProtocolVersion = %d, want %d", res.ProtocolVersion, ProtocolVersion)
	}
}

func TestSpawnAndInitializeTimeout(t *testing.T) {
	c, _, err := SpawnAndInitialize(testCtx(t, 200*time.Millisecond), fakeCommand(t, "hang"),
		InitializeParams{ProtocolVersion: ProtocolVersion}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if c != nil {
		t.Error("expected nil client on handshake failure")
	}
	// SpawnAndInitialize owns teardown on failure; nothing for the caller to do.
}

// TestCloseReapsProcess shows Close kills and reaps a spawned process — the
// teardown SpawnAndInitialize relies on internally on failure.
func TestCloseReapsProcess(t *testing.T) {
	c, err := Spawn(fakeCommand(t, "hang"), nil)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if c.cmd.ProcessState == nil {
		t.Error("process not reaped after Close")
	}
	_ = c.Close() // idempotent
}

func TestClientPID(t *testing.T) {
	// A spawned process exposes its OS pid; an in-process test client has none.
	c, err := Spawn(fakeCommand(t, "hang"), nil)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if c.PID() <= 0 {
		t.Errorf("spawned client PID = %d, want > 0", c.PID())
	}
	// Once the process exits/closes, PID reports 0 so a caller never samples a
	// stale pid the OS may have reused.
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if pid := c.PID(); pid != 0 {
		t.Errorf("PID after Close = %d, want 0", pid)
	}

	inproc, _ := peerClient(t, nil)
	if inproc.PID() != 0 {
		t.Errorf("in-process client PID = %d, want 0", inproc.PID())
	}
}

func TestManagerPIDUnknownSession(t *testing.T) {
	m := NewManager(nil)
	if pid, ok := m.PID("nope"); ok || pid != 0 {
		t.Errorf("PID of unknown session = %d,%v, want 0,false", pid, ok)
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
