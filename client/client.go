// Package client dials the tend daemon and speaks its plugin->daemon JSON-RPC
// contract. It is the shared connection layer for every tend client — the CLI
// and the MCP bridge in this module, and the standalone tend-ui in its own
// module (see docs/adr/0005). A client dials the socket, negotiates the contract
// version, registers its identity/role, and then issues typed Calls; this
// package owns that sequence so callers depend on it rather than reimplementing
// the handshake or reaching into tend's internal rpc package.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/handshake"
	"github.com/dusto/tend/internal/rpc"
)

// Conn is a live connection to the daemon. It wraps the JSON-RPC transport so
// callers depend on this package's surface rather than the internal rpc types.
type Conn struct {
	rc *rpc.Conn
}

// Call invokes a plugin->daemon method and decodes the reply into result (which
// may be nil for a method with no result). A daemon-side JSON-RPC error is
// returned as *Error, so callers can branch on its Code (see IsCode / AsError)
// without importing tend's internal packages.
func (c *Conn) Call(ctx context.Context, method string, params, result any) error {
	return asClientError(c.rc.Call(ctx, method, params, result))
}

// Close closes the connection.
func (c *Conn) Close() error { return c.rc.Close() }

// Done is closed when the connection terminates (peer closed, read error, or
// Close). Callers that follow a session for its lifetime select on it.
func (c *Conn) Done() <-chan struct{} { return c.rc.Done() }

// DefaultSocket returns the daemon's default socket path
// ($XDG_RUNTIME_DIR/tend/tend.sock), for a client that wants to show or reuse it.
func DefaultSocket() string { return rpc.SocketPath() }

// Options configures a Dial.
type Options struct {
	// Socket is the daemon socket path; empty uses DefaultSocket.
	Socket string
	// ClientID is the client's stable id (e.g. "tend-cli", "tend-ui"); a
	// reconnecting client reuses it to keep its identity.
	ClientID string
	// Role is the client role; the zero value registers as an observer (read-only,
	// serves no editor-local operations).
	Role api.ClientRole
	// MinPluginToDaemon is the lowest plugin->daemon contract version the caller's
	// methods need. The handshake fails if the daemon is older, so a version
	// mismatch surfaces at connect rather than at a later missing-method call.
	MinPluginToDaemon string
	// PromptCapable declares that this client can answer approval/clarification
	// prompts. Observers may see prompts but only prompt-capable clients resolve
	// them.
	PromptCapable bool
	// OnNotify receives daemon->client notifications by method name and raw
	// params — event.push (a subscribed stream's events), event.subscription_closed,
	// and prompt.raise. It MUST be set by a client that calls events.subscribe;
	// otherwise those pushes are delivered to a nil handler and dropped. The
	// callback runs on the connection's read goroutine, so it should hand work off
	// rather than block. Leave nil for a client that only makes one-shot calls
	// (the CLI, the MCP bridge). The caller decodes params (e.g. api.EventPushParams).
	OnNotify func(method string, params json.RawMessage)
}

// Dial connects to the daemon, performs the version handshake against
// MinPluginToDaemon, and registers the client. The caller owns the returned Conn
// and must Close it.
func Dial(ctx context.Context, opts Options) (*Conn, error) {
	socket := opts.Socket
	if socket == "" {
		socket = rpc.SocketPath()
	}
	nc, err := net.Dial("unix", socket)
	if err != nil {
		return nil, fmt.Errorf("connecting to tendd at %s: %w (is the daemon running?)", socket, err)
	}
	// A subscribing client (e.g. tend-ui following session streams) must receive
	// daemon->client pushes; route them to OnNotify. A one-shot caller leaves
	// OnNotify nil and gets the nil handler (inbound pushes, if any, are ignored).
	var h rpc.Handler
	if opts.OnNotify != nil {
		onNotify := opts.OnNotify
		h = rpc.HandlerFunc(func(_ context.Context, req *rpc.Request) (any, error) {
			// This client serves no daemon->client *requests* (those go to
			// editor-role clients). Reject any with method-not-found rather than a
			// bogus null success — mirrors rpc.Conn's nil-handler path. Only
			// notifications (event.push and friends) reach OnNotify.
			if !req.Notification {
				return nil, &rpc.Error{Code: rpc.CodeMethodNotFound, Message: "method not found: " + req.Method}
			}
			onNotify(req.Method, req.Params)
			return nil, nil
		})
	}
	rc := rpc.NewConn(nc, h)
	if _, err := handshake.Do(ctx, rc, api.Versions{PluginToDaemon: opts.MinPluginToDaemon}); err != nil {
		_ = rc.Close()
		return nil, fmt.Errorf("daemon handshake: %w", err)
	}
	role := opts.Role
	if role == "" {
		role = api.RoleObserver
	}
	if err := rc.Call(ctx, "client.register", api.ClientRegisterParams{
		ClientID:      api.ClientID(opts.ClientID),
		Role:          role,
		PromptCapable: opts.PromptCapable,
	}, &api.ClientRegisterResult{}); err != nil {
		_ = rc.Close()
		return nil, fmt.Errorf("client.register: %w", err)
	}
	return &Conn{rc: rc}, nil
}
