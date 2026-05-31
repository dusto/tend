// Package daemon assembles the tend daemon's serving loop: it accepts
// connections on a listener and serves each as a bidirectional JSON-RPC peer.
// Each connection gets its own plugin->daemon dispatch.Mux with the connect
// handshake and workspace methods registered, so per-connection state (such as
// the active workspace) is isolated between clients.
package daemon

import (
	"net"
	"sync"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/dispatch"
	"github.com/dusto/tend/internal/handshake"
	"github.com/dusto/tend/internal/rpc"
	"github.com/dusto/tend/internal/workspace"
)

// Server accepts connections on a listener and serves each over JSON-RPC. It is
// safe for concurrent use; Shutdown is idempotent.
type Server struct {
	ln        net.Listener
	epoch     string
	validator *dispatch.Validator

	mu     sync.Mutex
	conns  map[*rpc.Conn]struct{}
	closed bool
	wg     sync.WaitGroup
}

// New builds a Server over ln with a fresh per-process epoch and the params
// validator available for the build. It validates handler registration up front
// so a registration bug fails at startup rather than per connection.
func New(ln net.Listener) (*Server, error) {
	s := &Server{
		ln:        ln,
		epoch:     rpc.NewEpoch(),
		validator: newValidator(),
		conns:     make(map[*rpc.Conn]struct{}),
	}
	if _, err := s.newMux(); err != nil {
		return nil, err
	}
	return s, nil
}

// newMux builds a fresh plugin->daemon Mux for one connection: it registers the
// connect handshake and the workspace methods (with a per-connection workspace
// Manager) and enables params validation when a validator is available.
func (s *Server) newMux() (*dispatch.Mux, error) {
	mux := dispatch.NewMux(api.PluginToDaemon)
	if err := handshake.Register(mux, s.epoch); err != nil {
		return nil, err
	}
	if err := workspace.Register(mux, workspace.NewManager(s.epoch)); err != nil {
		return nil, err
	}
	if s.validator != nil {
		mux.UseValidator(s.validator)
	}
	return mux, nil
}

// Epoch returns the daemon's process epoch.
func (s *Server) Epoch() string { return s.epoch }

// Serve accepts connections until the listener is closed (for example by
// Shutdown), serving each as a JSON-RPC peer. It returns nil on a clean
// shutdown and the accept error otherwise.
func (s *Server) Serve() error {
	for {
		nc, err := s.ln.Accept()
		if err != nil {
			if s.isClosed() {
				return nil
			}
			return err
		}
		mux, err := s.newMux()
		if err != nil {
			_ = nc.Close()
			return err // deterministic registration failure
		}
		c := rpc.NewConn(nc, mux)
		if !s.add(c) {
			_ = c.Close()
			return nil
		}
		go func() {
			defer s.wg.Done()
			<-c.Done()
			s.remove(c)
		}()
	}
}

// Shutdown stops accepting, closes the listener and every open connection, and
// waits for their serving goroutines to finish. It is idempotent.
func (s *Server) Shutdown() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	_ = s.ln.Close()
	conns := make([]*rpc.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()

	for _, c := range conns {
		_ = c.Close()
	}
	s.wg.Wait()
}

func (s *Server) add(c *rpc.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.conns[c] = struct{}{}
	// Increment under the same lock that publishes the conn so Shutdown, which
	// snapshots conns under this lock before Wait, can never miss it.
	s.wg.Add(1)
	return true
}

func (s *Server) remove(c *rpc.Conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
}

func (s *Server) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *Server) connCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}
