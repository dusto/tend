// Package dispatch routes inbound JSON-RPC requests to per-method handlers,
// validating each method against the api contract for a single direction's
// method set. It sits between the transport (internal/rpc) and the daemon's
// method implementations. A Mux serves exactly one direction's methods (for
// example api.PluginToDaemon) and dispatches inbound requests for that
// direction by method name.
package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/rpc"
)

// Func handles one inbound method: it receives the raw params and returns a
// result to marshal (nil for none) or an error. Returning an *rpc.Error sends
// that JSON-RPC error verbatim.
type Func func(ctx context.Context, params json.RawMessage) (any, error)

// Mux routes inbound requests for a single direction's method set to registered
// handlers. It implements rpc.Handler, so it can be passed to rpc.NewConn.
type Mux struct {
	serves api.Direction
	known  map[string]api.Method

	mu       sync.RWMutex
	handlers map[string]Func
}

// NewMux creates a Mux that serves the given direction's methods (those are the
// only methods it will accept registrations for or dispatch).
func NewMux(serves api.Direction) *Mux {
	known := make(map[string]api.Method)
	for _, m := range api.Methods {
		if m.Direction == serves {
			known[m.Name] = m
		}
	}
	return &Mux{serves: serves, known: known, handlers: make(map[string]Func)}
}

// Register binds a handler to a method. The method must exist in the api
// contract for this Mux's direction, and may be registered only once.
func (m *Mux) Register(method string, fn Func) error {
	if _, ok := m.known[method]; !ok {
		return fmt.Errorf("dispatch: %q is not a %s method in the api contract", method, m.serves)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, dup := m.handlers[method]; dup {
		return fmt.Errorf("dispatch: handler already registered for %q", method)
	}
	m.handlers[method] = fn
	return nil
}

// Handle implements rpc.Handler, dispatching req to the registered handler.
func (m *Mux) Handle(ctx context.Context, req *rpc.Request) (any, error) {
	m.mu.RLock()
	fn, ok := m.handlers[req.Method]
	m.mu.RUnlock()
	if !ok {
		return nil, &rpc.Error{Code: rpc.CodeMethodNotFound, Message: "method not found: " + req.Method}
	}
	return fn(ctx, req.Params)
}

// Registered reports whether a handler is registered for method.
func (m *Mux) Registered(method string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.handlers[method]
	return ok
}

// Handle registers a typed handler for method: params are unmarshaled into P and
// the returned R is marshaled as the result. Malformed params yield an
// invalid-params JSON-RPC error. The method is validated like Mux.Register.
func Handle[P, R any](m *Mux, method string, fn func(context.Context, P) (R, error)) error {
	return m.Register(method, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p P
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "invalid params: " + err.Error()}
			}
		}
		return fn(ctx, p)
	})
}
