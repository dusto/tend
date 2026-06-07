// Package harness provides reusable test infrastructure for milestone-0
// integration tests: a fake ACP server and a fake bd, plus helpers to start a
// real daemon wired to them and drive it over the socket as a client. The fake
// ACP server runs as a re-exec of the importing test binary (its TestMain calls
// RunFakeACP when the fake-mode env is set); the daemon spawns it via the config
// from FakeACPConfig.
package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/acp"
	"github.com/dusto/tend/internal/daemon"
	"github.com/dusto/tend/internal/rpc"
)

// Environment variables that switch the re-exec'd test binary into a fake mode.
const (
	EnvFakeACP = "TEND_FAKE_ACP"
	EnvFakeBd  = "TEND_FAKE_BD"
)

// RunFakeACP runs the fake ACP agent over stdio: it serves initialize and
// session/new, and on session/prompt streams a milestone-0 turn — two message
// chunks, a tool call and its completion — before returning end_turn. It is the
// re-exec entrypoint a test binary's TestMain dispatches to when EnvFakeACP is
// set, so the daemon can spawn it as a real provider process.
func RunFakeACP() {
	h := rpc.HandlerFunc(func(ctx context.Context, req *rpc.Request) (any, error) {
		switch req.Method {
		case "initialize":
			return acp.InitializeResult{ProtocolVersion: acp.ProtocolVersion}, nil
		case acp.MethodNewSession:
			return acp.NewSessionResult{SessionID: "sess-1"}, nil
		case acp.MethodPrompt:
			var p acp.PromptParams
			_ = json.Unmarshal(req.Params, &p)
			conn := rpc.ConnFromContext(ctx)
			send := func(update map[string]any) {
				_ = conn.Notify(context.Background(), acp.SessionUpdateMethod, map[string]any{
					"sessionId": p.SessionID, "update": update,
				})
			}
			send(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"text": "hello "}})
			send(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"text": "world"}})
			send(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "t1", "title": "read_file"})
			send(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t1", "status": "completed"})
			return acp.PromptResult{StopReason: "end_turn"}, nil
		}
		return nil, &rpc.Error{Code: rpc.CodeMethodNotFound, Message: "fake acp: " + req.Method}
	})
	conn := rpc.NewConn(stdio{}, h)
	<-conn.Done()
}

// stdio is the fake agent's transport: read stdin, write stdout.
type stdio struct{}

func (stdio) Read(b []byte) (int, error)  { return os.Stdin.Read(b) }
func (stdio) Write(b []byte) (int, error) { return os.Stdout.Write(b) }
func (stdio) Close() error                { return nil }

// FakeACPConfig returns an ACP config whose only provider, "codex", is exe run
// with EnvFakeACP set — so a daemon built WithACPConfig(FakeACPConfig(exe))
// spawns the fake ACP server instead of the real codex. exe is normally the test
// binary (os.Executable()).
func FakeACPConfig(exe string) *acp.Config {
	return &acp.Config{ACP: acp.Settings{Providers: []acp.Provider{{
		ID:      "codex",
		Command: exe,
		Env:     []string{EnvFakeACP + "=1"},
		CwdMode: acp.CwdInherit,
		Enabled: true,
	}}}}
}

// RunFakeBd acts as the bd CLI: it reads its subcommand from the arguments and
// prints canned JSON. It is stateless (good for wiring tests, not round-trip
// state). It is the re-exec entrypoint for EnvFakeBd.
func RunFakeBd() {
	args := os.Args[1:]
	if len(args) == 0 {
		os.Exit(0)
	}
	switch args[0] {
	case "create":
		title := ""
		if len(args) > 1 {
			title = args[1]
		}
		fmt.Printf(`{"id":"fake-1","title":%q,"status":"open"}`, title)
	case "show":
		fmt.Print(`[{"id":"fake-1","title":"t","status":"open","description":"d"}]`)
	case "list":
		fmt.Print(`[{"id":"fake-1","title":"t","status":"open"}]`)
	}
	os.Exit(0)
}

// StartServer starts a daemon over a fresh unix socket in dir and serves it in
// the background. The caller shuts it down. It returns the socket path.
func StartServer(dir string, opts ...daemon.Option) (*daemon.Server, string, error) {
	sock := filepath.Join(dir, "tend.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return nil, "", err
	}
	srv, err := daemon.New(ln, filepath.Join(dir, "events.log"), opts...)
	if err != nil {
		_ = ln.Close()
		return nil, "", err
	}
	go func() { _ = srv.Serve() }()
	return srv, sock, nil
}

// Client is a connected test client. It dispatches daemon->client notifications
// (event.push, prompt.raise) into collectors so tests can assert on them.
type Client struct {
	conn *rpc.Conn

	mu      chan struct{} // 1-slot mutex
	events  []api.Event
	prompts []api.PromptRaiseParams
}

// Dial connects to the daemon at sock.
func Dial(sock string) (*Client, error) {
	nc, err := net.Dial("unix", sock)
	if err != nil {
		return nil, err
	}
	c := &Client{mu: make(chan struct{}, 1)}
	c.mu <- struct{}{}
	c.conn = rpc.NewConn(nc, rpc.HandlerFunc(c.handle))
	return c, nil
}

// Close closes the connection.
func (c *Client) Close() error { return c.conn.Close() }

func (c *Client) handle(_ context.Context, req *rpc.Request) (any, error) {
	switch req.Method {
	case "event.push":
		var p api.EventPushParams
		if json.Unmarshal(req.Params, &p) == nil {
			c.lock()
			c.events = append(c.events, p.Event)
			c.unlock()
		}
	case "prompt.raise":
		var p api.PromptRaiseParams
		if json.Unmarshal(req.Params, &p) == nil {
			c.lock()
			c.prompts = append(c.prompts, p)
			c.unlock()
		}
	}
	return nil, nil
}

func (c *Client) lock()   { <-c.mu }
func (c *Client) unlock() { c.mu <- struct{}{} }

// Call invokes a daemon method.
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	return c.conn.Call(ctx, method, params, result)
}

// Events returns the collected events on a stream, in arrival order.
func (c *Client) Events(stream api.StreamID) []api.Event {
	c.lock()
	defer c.unlock()
	var out []api.Event
	for _, ev := range c.events {
		if ev.StreamID == stream {
			out = append(out, ev)
		}
	}
	return out
}

// EventTypes returns the types of collected events on a stream, in arrival order.
func (c *Client) EventTypes(stream api.StreamID) []string {
	c.lock()
	defer c.unlock()
	var out []string
	for _, ev := range c.events {
		if ev.StreamID == stream {
			out = append(out, ev.Type)
		}
	}
	return out
}

// Prompts returns the prompts raised to this client.
func (c *Client) Prompts() []api.PromptRaiseParams {
	c.lock()
	defer c.unlock()
	return append([]api.PromptRaiseParams(nil), c.prompts...)
}

// WaitEventCount waits until at least n events have arrived on a stream.
func (c *Client) WaitEventCount(stream api.StreamID, n int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if len(c.EventTypes(stream)) >= n {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}
