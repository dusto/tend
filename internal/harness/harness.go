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
	"sync/atomic"
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
	var nextSession atomic.Int64
	h := rpc.HandlerFunc(func(ctx context.Context, req *rpc.Request) (any, error) {
		switch req.Method {
		case "initialize":
			return acp.InitializeResult{ProtocolVersion: acp.ProtocolVersion}, nil
		case acp.MethodNewSession:
			// Enforce the ACP contract a real agent enforces: mcpServers is a
			// required key (empty array allowed). Rejecting its absence keeps the
			// fake faithful, so a regression that drops the field is caught here
			// rather than only against a live agent.
			var raw map[string]json.RawMessage
			_ = json.Unmarshal(req.Params, &raw)
			if _, ok := raw["mcpServers"]; !ok {
				return nil, &rpc.Error{Code: -32602, Message: "fake acp: session/new missing required mcpServers"}
			}
			// Globally unique: pid distinguishes processes (the pool may spawn
			// several for one {workspace, provider} under concurrent starts) and the
			// counter distinguishes sessions within a process, so no two sessions
			// ever share an id (or therefore an event stream).
			return acp.NewSessionResult{SessionID: fmt.Sprintf("sess-%d-%d", os.Getpid(), nextSession.Add(1))}, nil
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
			// A prompt of "hold:<path>" keeps the turn open (session stays
			// running) until <path> appears, so a test can drive a mid-turn,
			// approval-gated edit before the turn ends. Deterministic: the wait is
			// on a file the test creates, not a sleep.
			if release, ok := holdPath(p.Prompt); ok {
				waitForFile(release, 10*time.Second)
			}
			return acp.PromptResult{StopReason: "end_turn"}, nil
		}
		return nil, &rpc.Error{Code: rpc.CodeMethodNotFound, Message: "fake acp: " + req.Method}
	})
	conn := rpc.NewConn(stdio{}, h)
	<-conn.Done()
}

// holdPath returns the release-file path encoded in a "hold:<path>" prompt, so
// the fake agent knows to keep the turn open until that file appears.
func holdPath(prompt []json.RawMessage) (string, bool) {
	if len(prompt) == 0 {
		return "", false
	}
	var block struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(prompt[0], &block) != nil {
		return "", false
	}
	const marker = "hold:"
	if len(block.Text) > len(marker) && block.Text[:len(marker)] == marker {
		return block.Text[len(marker):], true
	}
	return "", false
}

// waitForFile polls until path exists or the deadline passes.
func waitForFile(path string, d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
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
// (event.push, prompt.raise) into collectors so tests can assert on them, and —
// when it acts as a bound editor — answers the daemon->editor reverse requests
// (editor.*) from a settable buffer/diagnostics state.
type Client struct {
	conn *rpc.Conn

	mu      chan struct{} // 1-slot mutex
	events  []api.Event
	prompts []api.PromptRaiseParams

	// Editor state served on daemon->editor reverse calls. buffers holds the
	// files this editor reports as open (content + changedtick); diagnostics
	// holds per-file LSP diagnostics; current is the active buffer's uri.
	buffers     map[string]editorBuffer
	diagnostics map[string][]api.Diagnostic
	current     string
	wroteBuffer map[string]string // uri -> last content written via editor.write_buffer
}

type editorBuffer struct {
	content string
	tick    int64
}

// Dial connects to the daemon at sock.
func Dial(sock string) (*Client, error) {
	nc, err := net.Dial("unix", sock)
	if err != nil {
		return nil, err
	}
	c := &Client{
		mu:          make(chan struct{}, 1),
		buffers:     make(map[string]editorBuffer),
		diagnostics: make(map[string][]api.Diagnostic),
		wroteBuffer: make(map[string]string),
	}
	c.mu <- struct{}{}
	c.conn = rpc.NewConn(nc, rpc.HandlerFunc(c.handle))
	return c, nil
}

// SetOpenBuffer makes this editor report uri as an open buffer with the given
// content (and a fixed changedtick), and marks it the current buffer. It is how
// a test arms the editor before a reverse call reads it.
func (c *Client) SetOpenBuffer(uri, content string) {
	c.lock()
	defer c.unlock()
	c.buffers[uri] = editorBuffer{content: content, tick: 1}
	c.current = uri
}

// SetDiagnostics sets the diagnostics this editor reports for uri.
func (c *Client) SetDiagnostics(uri string, diags []api.Diagnostic) {
	c.lock()
	defer c.unlock()
	c.diagnostics[uri] = diags
}

// WroteBuffer returns the content last written to uri via editor.write_buffer,
// so a test can confirm an approved open-buffer edit was applied through the
// reverse call rather than to disk.
func (c *Client) WroteBuffer(uri string) (string, bool) {
	c.lock()
	defer c.unlock()
	v, ok := c.wroteBuffer[uri]
	return v, ok
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
	case "editor.current_buffer":
		c.lock()
		defer c.unlock()
		return api.EditorCurrentBufferResult{URI: c.current}, nil
	case "editor.read_buffer":
		var p api.EditorReadBufferParams
		_ = json.Unmarshal(req.Params, &p)
		c.lock()
		defer c.unlock()
		if b, ok := c.buffers[p.URI]; ok {
			tick := b.tick
			return api.EditorReadBufferResult{Content: b.content, Base: api.FileBase{ChangedTick: &tick}, Open: true}, nil
		}
		// Not open here: the daemon falls back to disk.
		return api.EditorReadBufferResult{Open: false}, nil
	case "editor.write_buffer":
		var p api.EditorWriteBufferParams
		_ = json.Unmarshal(req.Params, &p)
		c.lock()
		defer c.unlock()
		c.wroteBuffer[p.URI] = p.Content
		next := c.buffers[p.URI].tick + 1
		c.buffers[p.URI] = editorBuffer{content: p.Content, tick: next}
		return api.EditorWriteBufferResult{Base: api.FileBase{ChangedTick: &next}}, nil
	case "editor.diagnostics":
		var p api.EditorDiagnosticsParams
		_ = json.Unmarshal(req.Params, &p)
		c.lock()
		defer c.unlock()
		_, open := c.buffers[p.URI]
		return api.EditorDiagnosticsResult{URI: p.URI, Open: open, Diagnostics: c.diagnostics[p.URI]}, nil
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

// WaitPrompt waits until at least one prompt has been raised and returns the
// first. A mutating call (e.g. file.patch) blocks on the approval gate, so a
// test issues it on another goroutine, waits here for the prompt, then resolves
// it with approval.respond.
func (c *Client) WaitPrompt(d time.Duration) (api.PromptRaiseParams, bool) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if p := c.Prompts(); len(p) > 0 {
			return p[0], true
		}
		time.Sleep(time.Millisecond)
	}
	return api.PromptRaiseParams{}, false
}
