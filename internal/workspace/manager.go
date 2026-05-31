package workspace

import (
	"context"
	"errors"
	"sync"

	"github.com/dusto/tend/api"
)

// ErrNoActiveWorkspace reports that Current was called before any workspace was
// opened.
var ErrNoActiveWorkspace = errors.New("workspace: no active workspace")

// Manager tracks the workspaces opened over one connection and which is active.
// A workspace is made active by Open; Current reports it. It is safe for
// concurrent use.
type Manager struct {
	epoch api.DaemonEpoch

	mu      sync.Mutex
	opened  map[api.WorkspaceID]Workspace
	current *api.WorkspaceID
}

// NewManager returns a Manager that stamps the given daemon epoch onto the
// workspace info it reports.
func NewManager(epoch string) *Manager {
	return &Manager{
		epoch:  api.DaemonEpoch(epoch),
		opened: make(map[api.WorkspaceID]Workspace),
	}
}

// Open resolves dir, records the workspace, makes it the active workspace, and
// returns its info.
func (m *Manager) Open(ctx context.Context, dir string) (api.WorkspaceInfo, error) {
	w, err := Resolve(ctx, dir)
	if err != nil {
		return api.WorkspaceInfo{}, err
	}
	m.mu.Lock()
	m.opened[w.WorkspaceID] = w
	id := w.WorkspaceID
	m.current = &id
	m.mu.Unlock()
	return m.info(w), nil
}

// Current returns the active workspace, or ErrNoActiveWorkspace when none has
// been opened.
func (m *Manager) Current() (api.WorkspaceInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil {
		return api.WorkspaceInfo{}, ErrNoActiveWorkspace
	}
	return m.info(m.opened[*m.current]), nil
}

// Lookup returns a previously opened workspace by id. Mutating handlers use it
// to find the target workspace and call Workspace.EnsureMutable before acting.
func (m *Manager) Lookup(id api.WorkspaceID) (Workspace, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w, ok := m.opened[id]
	return w, ok
}

func (m *Manager) info(w Workspace) api.WorkspaceInfo {
	return api.WorkspaceInfo{
		WorkspaceID:  w.WorkspaceID,
		WorktreeRoot: w.WorktreeRoot,
		WorktreeID:   w.WorktreeID,
		Ephemeral:    w.Ephemeral,
		DaemonEpoch:  m.epoch,
	}
}
