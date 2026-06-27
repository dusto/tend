// Package daemon assembles the tend daemon's serving loop: it accepts
// connections on a listener and serves each as a bidirectional JSON-RPC peer.
// Each connection gets its own plugin->daemon dispatch.Mux with the connect
// handshake and workspace methods registered, so per-connection state (such as
// the active workspace) is isolated between clients.
package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/acp"
	"github.com/dusto/tend/internal/agent"
	"github.com/dusto/tend/internal/approvals"
	"github.com/dusto/tend/internal/client"
	"github.com/dusto/tend/internal/dispatch"
	"github.com/dusto/tend/internal/editor"
	"github.com/dusto/tend/internal/events"
	"github.com/dusto/tend/internal/files"
	"github.com/dusto/tend/internal/handshake"
	"github.com/dusto/tend/internal/lsp"
	"github.com/dusto/tend/internal/pty"
	"github.com/dusto/tend/internal/rpc"
	"github.com/dusto/tend/internal/session"
	"github.com/dusto/tend/internal/sessions"
	"github.com/dusto/tend/internal/tasks"
	"github.com/dusto/tend/internal/workspace"
)

// maxProcsPerProvider bounds the provider processes the pool keeps per
// {workspace, provider}. Many task-scoped sessions may share one process, so a
// small cap is enough.
const maxProcsPerProvider = 8

// approvalTTL is how long a pending approval's payload advertises before it is
// considered expired. Enforcing the deadline is layered on separately.
const approvalTTL = 5 * time.Minute

// Server accepts connections on a listener and serves each over JSON-RPC. It is
// safe for concurrent use; Shutdown is idempotent.
type Server struct {
	ln        net.Listener
	epoch     string
	validator *dispatch.Validator
	store     *events.Store
	log       *events.Log

	// Agent stack: shared across connections because sessions are workspace-bound
	// and outlive any single client.
	sessions *session.Registry
	pool     *acp.Pool
	agent    *agent.Service
	// clients tracks connected-client identity/capabilities daemon-wide.
	clients *client.Registry
	// binder owns editor-binding decisions across sessions; gate is the shared
	// approval gate; files serves repo-file tools (editor-aware reads and gated
	// mutations) over the editor reverse-RPC and the gate.
	binder  *editor.Binder
	gate    *approvals.Gate
	files   *files.Service
	lsp     *lsp.Service
	sessSvc *sessions.Service
	tasks   *tasks.Service
	panes   *pty.Service
	ptyMgr  *pty.Manager

	mu     sync.Mutex
	conns  map[*rpc.Conn]struct{}
	closed bool
	wg     sync.WaitGroup
}

// Option configures a Server at construction. The zero configuration uses the
// default ACP providers and the in-memory fake task provider; tests inject a
// fake ACP config and/or task factory.
type Option func(*options)

type options struct {
	acp         *acp.Config
	taskFactory tasks.Factory
	retention   uint64
}

// WithACPConfig sets the ACP provider config the daemon spawns from (default:
// acp.DefaultConfig).
func WithACPConfig(c *acp.Config) Option { return func(o *options) { o.acp = c } }

// WithTaskFactory sets the per-workspace task provider factory (default: an
// in-memory fake provider).
func WithTaskFactory(f tasks.Factory) Option { return func(o *options) { o.taskFactory = f } }

// WithEventRetention sets how many most-recent raw event records each stream
// keeps uncompacted (default: events.DefaultRetention).
func WithEventRetention(n uint64) Option { return func(o *options) { o.retention = n } }

// New builds a Server over ln with a fresh per-process epoch and the params
// validator available for the build. It opens the durable event log at logPath
// and validates handler registration up front so a registration bug fails at
// startup rather than per connection.
func New(ln net.Listener, logPath string, opts ...Option) (*Server, error) {
	o := options{
		acp:         acp.DefaultConfig(),
		taskFactory: func(ws api.WorkspaceID) tasks.Provider { return tasks.NewFake(ws) },
		retention:   events.DefaultRetention,
	}
	for _, opt := range opts {
		opt(&o)
	}

	log, err := events.OpenLog(logPath)
	if err != nil {
		return nil, err
	}
	s := &Server{
		ln:        ln,
		epoch:     rpc.NewEpoch(),
		validator: newValidator(),
		store:     events.NewStore(log),
		log:       log,
		sessions:  session.NewRegistry(),
		clients:   client.NewRegistry(),
		conns:     make(map[*rpc.Conn]struct{}),
	}
	s.store.SetRetention(o.retention)
	s.binder = editor.NewBinder(s.sessions, s.clients)
	s.gate = approvals.NewGate(s.store, approvals.Options{
		TTL:      approvalTTL,
		Prompter: &promptBroadcaster{clients: s.clients},
	})
	editors := editor.NewService(s.binder, s.clients)
	s.files = files.NewService(s.sessions, editors, s.gate, files.Options{})
	s.lsp = lsp.NewService(s.sessions, editors)
	s.sessSvc = sessions.NewService(s.sessions, s.binder)
	// Task provider per workspace. The in-memory fake stands in until the beads
	// adapter is wired; the task.* contract and event bridge are independent of it.
	s.tasks = tasks.NewService(o.taskFactory, s.store)
	s.ptyMgr = pty.NewManager()
	s.panes = pty.NewService(s.ptyMgr, s.sessions, s.gate, s.store, "")

	// Assemble the shared agent stack: a normalizer that streams turn output to
	// the event store, a process pool that spawns providers with that normalizer
	// installed, and the session manager/service over it.
	norm := acp.NewNormalizer(s.store, nil)
	s.pool = acp.NewPool(spawnProvider(o.acp, norm), s.store, acp.Options{Max: maxProcsPerProvider})
	s.agent = agent.NewService(s.sessions, acp.NewManager(s.pool), norm)

	if _, _, err := s.newMux(); err != nil {
		_ = log.Close()
		_ = s.pool.Close()
		return nil, err
	}
	return s, nil
}

// spawnProvider returns the pool's SpawnFunc: it launches the configured provider
// for a key and runs the ACP initialize handshake, installing h as the inbound
// handler so the agent's session/update notifications are normalized to events.
func spawnProvider(cfg *acp.Config, h rpc.Handler) acp.SpawnFunc {
	return func(ctx context.Context, key acp.Key) (acp.Process, error) {
		prov, ok := cfg.Provider(string(key.Provider))
		if !ok || !prov.Enabled {
			return nil, fmt.Errorf("daemon: unknown or disabled provider %q", key.Provider)
		}
		// CwdWorkspace providers run in the worktree root of the session that
		// triggered the spawn; the workspace id (the common git dir) is only a
		// fallback when no root was supplied.
		root := acp.WorktreeRootFromContext(ctx)
		if root == "" {
			root = string(key.Workspace)
		}
		cmd := prov.LaunchCommand(root)
		params := acp.InitializeParams{
			ProtocolVersion:    acp.ProtocolVersion,
			ClientCapabilities: acp.ClientCapabilities{FS: acp.FSCapabilities{ReadTextFile: true, WriteTextFile: true}},
		}
		cl, _, err := acp.SpawnAndInitialize(ctx, cmd, params, h)
		if err != nil {
			return nil, err
		}
		return cl, nil
	}
}

// newMux builds a fresh plugin->daemon Mux for one connection: it registers the
// connect handshake, the workspace methods (with a per-connection workspace
// Manager), the event subscription methods (with a per-connection Pusher over
// the shared event store), and the agent lifecycle methods (backed by the
// shared, daemon-wide agent service), and the client-registration method (with
// a per-connection identity handler over the shared client registry), and
// enables params validation when a validator is available. It returns a cleanup
// to run when the connection ends, which drops the connection's registered
// client identity.
func (s *Server) newMux() (*dispatch.Mux, func(), error) {
	mux := dispatch.NewMux(api.PluginToDaemon)
	if err := handshake.Register(mux, s.epoch); err != nil {
		return nil, nil, err
	}
	if err := workspace.Register(mux, workspace.NewManager(s.epoch)); err != nil {
		return nil, nil, err
	}
	if err := events.RegisterClient(mux, events.NewPusher(s.store)); err != nil {
		return nil, nil, err
	}
	// The per-connection client identity; agent.start and approval.respond both
	// resolve the calling client through cc.Self().
	cc := client.NewConn(s.clients)
	if err := cc.Register(mux); err != nil {
		return nil, nil, err
	}
	// agent.start binds the new session to its creating editor client, so
	// editor-local reverse calls (editor.*) route to it. A non-editor caller
	// fails Claim and the session stays headless.
	if err := agent.Register(mux, s.agent, func(sid api.SessionID) {
		if self, ok := cc.Self(); ok {
			_ = s.binder.Claim(sid, self.ID)
		}
	}); err != nil {
		return nil, nil, err
	}
	if err := files.Register(mux, s.files); err != nil {
		return nil, nil, err
	}
	if err := lsp.Register(mux, s.lsp); err != nil {
		return nil, nil, err
	}
	// session.list/claim resolve the calling client through this connection's
	// cc.Self() (empty when unregistered), so the listing reports editor binding
	// relative to the caller and claim binds it.
	if err := sessions.Register(mux, s.sessSvc, func() api.ClientID {
		if self, ok := cc.Self(); ok {
			return self.ID
		}
		return ""
	}); err != nil {
		return nil, nil, err
	}
	if err := tasks.Register(mux, s.tasks); err != nil {
		return nil, nil, err
	}
	if err := pty.Register(mux, s.panes); err != nil {
		return nil, nil, err
	}
	// approval.respond is capability-gated on the calling connection's identity.
	if err := approvals.RegisterResponder(mux, s.gate, func() (approvals.Responder, bool) {
		if self, ok := cc.Self(); ok {
			return self, true
		}
		return nil, false
	}); err != nil {
		return nil, nil, err
	}
	if s.validator != nil {
		mux.UseValidator(s.validator)
	}
	// On disconnect, drop the identity (ownership-checked) and, only if this
	// connection was the live owner of its client id, release the editor bindings
	// it held so those sessions go headless. A stale connection whose id already
	// reconnected on another connection removes nothing and releases nothing.
	cleanup := func() {
		self, _ := cc.Self()
		if cc.Close() {
			s.binder.ReleaseClient(self.ID)
		}
	}
	return mux, cleanup, nil
}

// Epoch returns the daemon's process epoch.
func (s *Server) Epoch() string { return s.epoch }

// EventStore returns the daemon's event store. It is exposed primarily for tests
// and administration (for example to drive compaction).
func (s *Server) EventStore() *events.Store { return s.store }

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
		mux, cleanup, err := s.newMux()
		if err != nil {
			_ = nc.Close()
			return err // deterministic registration failure
		}
		c := rpc.NewConn(nc, mux)
		if !s.add(c) {
			cleanup()
			_ = c.Close()
			return nil
		}
		slog.Debug("connection accepted")
		go func() {
			defer s.wg.Done()
			<-c.Done()
			cleanup()
			s.remove(c)
			slog.Debug("connection closed")
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
	_ = s.pool.Close()
	s.tasks.Close()
	s.ptyMgr.Shutdown()
	_ = s.log.Close()
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
