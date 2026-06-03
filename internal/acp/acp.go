package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"

	"github.com/dusto/tend/internal/rpc"
)

// ProtocolVersion is the ACP protocol version this client speaks.
const ProtocolVersion = 1

// Command describes how to launch an ACP provider process. It is
// provider-agnostic: the registry/pool supplies these fields from config.
type Command struct {
	Path string   // executable to run
	Args []string // arguments
	// Env is the process environment as "K=V" entries. Following os/exec, a nil
	// Env makes the process inherit the daemon's environment; pass a non-nil
	// slice (possibly empty) to set it explicitly. The provider/config layer is
	// responsible for composing this deliberately.
	Env []string
	Dir string // working directory ("" uses the caller's)
}

// Client is a JSON-RPC 2.0 peer to one ACP provider process over its stdio. ACP
// uses the same JSON-RPC framing as the rest of TEND, so the transport is an
// rpc.Conn over the process's stdout (reads) and stdin (writes). The client is
// provider-agnostic; provider-specific behavior lives above it.
type Client struct {
	conn *rpc.Conn
	cmd  *exec.Cmd // nil for an in-process client (tests)
}

// Spawn starts the ACP process described by c and returns a Client speaking
// JSON-RPC over its stdio. Inbound requests and notifications from the agent are
// dispatched to h (which may be nil to ignore notifications and reject inbound
// requests with method-not-found). On any setup error the process is torn down.
func Spawn(c Command, h rpc.Handler) (*Client, error) {
	cmd := exec.Command(c.Path, c.Args...)
	cmd.Env = c.Env
	cmd.Dir = c.Dir

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("acp: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("acp: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("acp: start %q: %w", c.Path, err)
	}
	return &Client{conn: rpc.NewConn(&procStdio{r: stdout, w: stdin}, h), cmd: cmd}, nil
}

// SpawnAndInitialize spawns the process and runs the initialize handshake,
// owning teardown: if spawning or the handshake fails (including an initialize
// timeout via ctx), the process is killed and reaped before returning. Use a
// deadline on ctx to bound the handshake. This is the path the process pool
// uses so a hung provider cannot leak.
func SpawnAndInitialize(ctx context.Context, c Command, params InitializeParams, h rpc.Handler) (*Client, InitializeResult, error) {
	cl, err := Spawn(c, h)
	if err != nil {
		return nil, InitializeResult{}, err
	}
	res, err := cl.Initialize(ctx, params)
	if err != nil {
		_ = cl.Close()
		return nil, InitializeResult{}, err
	}
	return cl, res, nil
}

// newClient wraps an existing stdio stream as a Client, for tests that drive a
// fake ACP peer in-process.
func newClient(rwc io.ReadWriteCloser, h rpc.Handler) *Client {
	return &Client{conn: rpc.NewConn(rwc, h)}
}

// Initialize performs the ACP initialize handshake, blocking until the agent
// replies, ctx is done (use a deadline for a timeout), or the connection closes.
// It does not tear the client down on failure; spawned callers should use
// SpawnAndInitialize, which owns that contract.
func (c *Client) Initialize(ctx context.Context, params InitializeParams) (InitializeResult, error) {
	var res InitializeResult
	if err := c.conn.Call(ctx, "initialize", params, &res); err != nil {
		return InitializeResult{}, err
	}
	return res, nil
}

// Call invokes an arbitrary ACP method on the agent.
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	return c.conn.Call(ctx, method, params, result)
}

// Notify sends an ACP notification to the agent.
func (c *Client) Notify(ctx context.Context, method string, params any) error {
	return c.conn.Notify(ctx, method, params)
}

// Done is closed when the transport is no longer usable (the process exited or
// Close was called).
func (c *Client) Done() <-chan struct{} { return c.conn.Done() }

// Close tears the client down: it closes the transport (which closes the
// process's stdin) and, for a spawned process, kills and reaps it. It is safe to
// call more than once.
func (c *Client) Close() error {
	_ = c.conn.Close()
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
	}
	return nil
}

// procStdio adapts a process's separate stdout (read) and stdin (write) streams
// into one io.ReadWriteCloser for rpc.Conn.
type procStdio struct {
	r io.ReadCloser
	w io.WriteCloser
}

func (p *procStdio) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p *procStdio) Write(b []byte) (int, error) { return p.w.Write(b) }

func (p *procStdio) Close() error {
	return errors.Join(p.w.Close(), p.r.Close())
}

// InitializeParams is the ACP initialize request: the protocol version the
// client speaks and the capabilities it offers the agent.
type InitializeParams struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientCapabilities ClientCapabilities `json:"clientCapabilities,omitzero"`
}

// ClientCapabilities advertises what the client (daemon) can do for the agent.
type ClientCapabilities struct {
	FS FSCapabilities `json:"fs,omitzero"`
}

// FSCapabilities advertises daemon-side file access the agent may request.
type FSCapabilities struct {
	ReadTextFile  bool `json:"readTextFile"`
	WriteTextFile bool `json:"writeTextFile"`
}

// InitializeResult is the agent's initialize reply. Agent capabilities and auth
// methods are kept raw here: the generic client does not interpret them, leaving
// provider-specific parsing to higher layers.
type InitializeResult struct {
	ProtocolVersion   int             `json:"protocolVersion"`
	AgentCapabilities json.RawMessage `json:"agentCapabilities,omitempty"`
	AuthMethods       json.RawMessage `json:"authMethods,omitempty"`
}
