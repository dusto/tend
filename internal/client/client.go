// Package client tracks connected-client identity and capabilities. Each
// connection registers a stable client id and declares whether it can serve
// editor-local operations (an editor) versus observe only, and whether it may
// respond to prompts. A daemon-wide Registry holds the currently connected
// clients so the daemon can route editor-local work to an editor, broadcast
// prompts, and capability-gate prompt responses; a per-connection Conn remembers
// the connection's own identity for caller checks.
package client

import (
	"context"
	"sync"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/dispatch"
	"github.com/dusto/tend/internal/rpc"
)

// Method is the client-registration method name.
const Method = "client.register"

// Capabilities is what a connected client declared at registration.
type Capabilities struct {
	Role          api.ClientRole
	PromptCapable bool
}

// Client is a connected client's identity and capabilities.
type Client struct {
	ID   api.ClientID
	Caps Capabilities
}

// IsEditor reports whether the client can serve editor-local operations.
func (c *Client) IsEditor() bool { return c.Caps.Role == api.RoleEditor }

// CanRespondToPrompts reports whether the client may resolve approval and
// clarification prompts.
func (c *Client) CanRespondToPrompts() bool { return c.Caps.PromptCapable }

// Registry holds the currently connected clients, keyed by client id. It is safe
// for concurrent use.
type Registry struct {
	mu      sync.Mutex
	clients map[api.ClientID]*Client
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{clients: make(map[api.ClientID]*Client)}
}

// Register records id with caps, replacing any previous entry for that id (a
// reconnecting client re-registers its identity), and returns the stored Client.
func (r *Registry) Register(id api.ClientID, caps Capabilities) *Client {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := &Client{ID: id, Caps: caps}
	r.clients[id] = c
	return c
}

// Remove drops the client with id.
func (r *Registry) Remove(id api.ClientID) {
	r.mu.Lock()
	delete(r.clients, id)
	r.mu.Unlock()
}

// Get returns the client with id, if present.
func (r *Registry) Get(id api.ClientID) (*Client, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.clients[id]
	return c, ok
}

// List returns the connected clients in unspecified order.
func (r *Registry) List() []*Client {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Client, 0, len(r.clients))
	for _, c := range r.clients {
		out = append(out, c)
	}
	return out
}

// Conn is the per-connection client-identity handler: it serves client.register
// for one connection, recording the registered identity in the shared registry
// and remembering it so other handlers on the connection can check the caller's
// capabilities. Close drops the identity from the registry when the connection
// ends.
type Conn struct {
	registry *Registry

	mu   sync.Mutex
	self *Client
}

// NewConn returns a per-connection handler backed by the shared registry.
func NewConn(registry *Registry) *Conn {
	return &Conn{registry: registry}
}

// Register installs client.register on m.
func (c *Conn) Register(m *dispatch.Mux) error {
	return dispatch.Handle(m, Method, c.register)
}

func (c *Conn) register(_ context.Context, p api.ClientRegisterParams) (api.ClientRegisterResult, error) {
	if p.ClientID == "" {
		return api.ClientRegisterResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "client: client_id is required"}
	}
	if !p.Role.Valid() {
		return api.ClientRegisterResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "client: invalid role " + string(p.Role)}
	}
	cl := c.registry.Register(p.ClientID, Capabilities{Role: p.Role, PromptCapable: p.PromptCapable})
	c.mu.Lock()
	c.self = cl
	c.mu.Unlock()
	return api.ClientRegisterResult{ClientID: cl.ID}, nil
}

// Self returns the identity registered on this connection, if any.
func (c *Conn) Self() (*Client, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.self, c.self != nil
}

// Close removes this connection's registered identity from the shared registry.
// It is the connection-teardown hook; it is safe to call when nothing was
// registered.
func (c *Conn) Close() {
	c.mu.Lock()
	self := c.self
	c.mu.Unlock()
	if self != nil {
		c.registry.Remove(self.ID)
	}
}
