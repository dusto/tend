// Package clienttest provides a fake tend daemon for testing external clients of
// the client package. A caller (e.g. tend-ui) registers method handlers and
// dials the returned socket with client.Dial, exercising real dial → handshake →
// register → call flows without hand-rolling the JSON-RPC wire or importing
// tend's internal packages.
package clienttest

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"sync"
	"testing"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/handshake"
	"github.com/dusto/tend/internal/rpc"
)

// Handler answers one method call: it receives the raw params and returns the
// result value (marshaled as the JSON-RPC result) or an error. Return an
// *rpc.Error via Errorf/ErrorCode to drive a specific daemon error code at the
// client boundary.
type Handler func(params json.RawMessage) (any, error)

// Server is a fake daemon listening on a unix socket. Register handlers with
// Handle, then dial Socket() with client.Dial. The version handshake
// (daemon.hello) and client.register are answered automatically. It is closed
// via t.Cleanup.
type Server struct {
	socket string

	mu       sync.RWMutex
	handlers map[string]Handler
	epoch    string
}

// New starts a Server on a temp unix socket and registers cleanup on t.
func New(t testing.TB) *Server {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "tend.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("clienttest: listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	s := &Server{socket: socket, handlers: map[string]Handler{}, epoch: "clienttest-epoch"}
	go s.serve(t, ln)
	return s
}

// Socket returns the path to dial with client.Options.Socket.
func (s *Server) Socket() string { return s.socket }

// Handle registers fn for method, replacing any prior handler. daemon.hello and
// client.register are handled internally and cannot be overridden.
func (s *Server) Handle(method string, fn Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[method] = fn
}

// ErrorCode returns a Handler-usable error carrying a specific JSON-RPC code and
// message, so a test can drive client.IsCode branches (e.g. api.ErrCursorCompacted).
func ErrorCode(code int, message string) error {
	return &rpc.Error{Code: code, Message: message}
}

func (s *Server) serve(t testing.TB, ln net.Listener) {
	m := s.mux(t)
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		sc := rpc.NewConn(conn, m)
		t.Cleanup(func() { _ = sc.Close() })
	}
}

// mux builds the inbound handler: the auto-answered handshake/register plus a
// dispatch to the registered method handlers.
func (s *Server) mux(t testing.TB) rpc.Handler {
	return rpc.HandlerFunc(func(_ context.Context, req *rpc.Request) (any, error) {
		switch req.Method {
		case handshake.Method:
			return api.HelloResult{Versions: api.CurrentVersions(), DaemonEpoch: api.DaemonEpoch(s.epoch)}, nil
		case "client.register":
			return api.ClientRegisterResult{}, nil
		}
		s.mu.RLock()
		fn, ok := s.handlers[req.Method]
		s.mu.RUnlock()
		if !ok {
			return nil, &rpc.Error{Code: rpc.CodeMethodNotFound, Message: "method not found: " + req.Method}
		}
		return fn(req.Params)
	})
}
