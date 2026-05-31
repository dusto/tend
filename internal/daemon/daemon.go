// Package daemon assembles the tend daemon's serving loop: it accepts
// connections on a listener and serves each as a bidirectional JSON-RPC peer,
// routing plugin->daemon methods through a single dispatch.Mux with the connect
// handshake registered.
package daemon

import (
	"net"
	"sync"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/dispatch"
	"github.com/dusto/tend/internal/handshake"
	"github.com/dusto/tend/internal/rpc"
)

// Server accepts connections on a listener and serves each over JSON-RPC,
// routing plugin->daemon methods through one Mux. It is safe for concurrent
// use; Shutdown is idempotent.
type Server struct {
	ln    net.Listener
	mux   *dispatch.Mux
	epoch string

	mu     sync.Mutex
	conns  map[*rpc.Conn]struct{}
	closed bool
	wg     sync.WaitGroup
}

// New builds a Server over ln: it creates a plugin->daemon Mux, registers the
// connect handshake with a fresh per-process epoch, and enables params
// validation when a validator is available for the build.
func New(ln net.Listener) (*Server, error) {
	epoch := rpc.NewEpoch()
	mux := dispatch.NewMux(api.PluginToDaemon)
	if err := handshake.Register(mux, epoch); err != nil {
		return nil, err
	}
	if v := newValidator(); v != nil {
		mux.UseValidator(v)
	}
	return &Server{
		ln:    ln,
		mux:   mux,
		epoch: epoch,
		conns: make(map[*rpc.Conn]struct{}),
	}, nil
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
		c := rpc.NewConn(nc, s.mux)
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
