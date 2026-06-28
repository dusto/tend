// Package provider implements the provider.* methods: provider.list reports the
// configured ACP providers and how many processes the pool holds for a
// workspace, and provider.start/stop warm or terminate a provider's pooled
// processes. The pool spawns lazily on the first turn, so these let a client
// pre-spawn a provider (for a snappier first prompt) or tear one down without
// ending its sessions through the agent lifecycle.
package provider

import (
	"context"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/acp"
	"github.com/dusto/tend/internal/dispatch"
	"github.com/dusto/tend/internal/rpc"
)

// Method names, matching the api contract.
const (
	MethodList  = "provider.list"
	MethodStart = "provider.start"
	MethodStop  = "provider.stop"
)

// Pool is the slice of the process pool the service drives. *acp.Pool satisfies
// it; it is an interface so the service can be tested without spawning provider
// processes.
type Pool interface {
	RunningFor(key acp.Key) int
	Start(ctx context.Context, key acp.Key) error
	StopKey(key acp.Key) int
}

// Service backs the provider.* methods over the provider config and the process
// pool. It is safe for concurrent use (its state is the shared config and pool).
type Service struct {
	cfg  *acp.Config
	pool Pool
}

// NewService returns a Service reading cfg and driving pool.
func NewService(cfg *acp.Config, pool Pool) *Service {
	return &Service{cfg: cfg, pool: pool}
}

// Register installs the provider.* methods on m, backed by s.
func Register(m *dispatch.Mux, s *Service) error {
	if err := dispatch.Handle(m, MethodList, s.List); err != nil {
		return err
	}
	if err := dispatch.Handle(m, MethodStart, s.Start); err != nil {
		return err
	}
	return dispatch.Handle(m, MethodStop, s.Stop)
}

// List returns the configured providers in definition order, each with its
// running-process count for the requested workspace.
func (s *Service) List(_ context.Context, p api.ProviderListParams) (api.ProviderListResult, error) {
	if p.WorkspaceID == "" {
		return api.ProviderListResult{}, invalidParams("workspace_id is required")
	}
	provs := s.cfg.ACP.Providers
	out := make([]api.ProviderInfo, 0, len(provs))
	for _, prov := range provs {
		out = append(out, api.ProviderInfo{
			ProviderID: api.ProviderID(prov.ID),
			Command:    prov.Command,
			Enabled:    prov.Enabled,
			Running:    s.pool.RunningFor(s.key(p.WorkspaceID, api.ProviderID(prov.ID))),
		})
	}
	return api.ProviderListResult{Providers: out}, nil
}

// Start warms a provider for a workspace, spawning a process if none is live,
// and returns its running-process count. It rejects an unknown or disabled
// provider — only a provider the daemon will actually spawn can be started.
func (s *Service) Start(ctx context.Context, p api.ProviderStartParams) (api.ProviderStartResult, error) {
	if p.WorkspaceID == "" {
		return api.ProviderStartResult{}, invalidParams("workspace_id is required")
	}
	if p.WorktreeRoot == "" {
		return api.ProviderStartResult{}, invalidParams("worktree_root is required")
	}
	prov, ok := s.cfg.Provider(string(p.ProviderID))
	if !ok {
		return api.ProviderStartResult{}, invalidParams("unknown provider " + string(p.ProviderID))
	}
	if !prov.Enabled {
		return api.ProviderStartResult{}, invalidParams("provider " + string(p.ProviderID) + " is disabled")
	}
	key := s.key(p.WorkspaceID, p.ProviderID)
	// Carry the worktree root so a CwdWorkspace provider warmed here starts in it
	// (and a later session reusing the idle process inherits that cwd), rather
	// than falling back to the workspace's common git dir. Mirrors agent.start.
	ctx = acp.WithWorktreeRoot(ctx, p.WorktreeRoot)
	if err := s.pool.Start(ctx, key); err != nil {
		return api.ProviderStartResult{}, internalErr(err)
	}
	return api.ProviderStartResult{ProviderID: p.ProviderID, Running: s.pool.RunningFor(key)}, nil
}

// Stop terminates a provider's pooled processes for a workspace and returns how
// many it closed. An unknown provider is a caller error; a configured provider
// with no live process stops zero, which is not an error (it is already stopped).
func (s *Service) Stop(_ context.Context, p api.ProviderStopParams) (api.ProviderStopResult, error) {
	if p.WorkspaceID == "" {
		return api.ProviderStopResult{}, invalidParams("workspace_id is required")
	}
	if _, ok := s.cfg.Provider(string(p.ProviderID)); !ok {
		return api.ProviderStopResult{}, invalidParams("unknown provider " + string(p.ProviderID))
	}
	stopped := s.pool.StopKey(s.key(p.WorkspaceID, p.ProviderID))
	return api.ProviderStopResult{ProviderID: p.ProviderID, Stopped: stopped}, nil
}

func (s *Service) key(ws api.WorkspaceID, prov api.ProviderID) acp.Key {
	return acp.Key{Workspace: ws, Provider: prov}
}

func invalidParams(msg string) *rpc.Error {
	return &rpc.Error{Code: rpc.CodeInvalidParams, Message: "provider: " + msg}
}

func internalErr(err error) *rpc.Error {
	return &rpc.Error{Code: rpc.CodeInternalError, Message: err.Error()}
}
