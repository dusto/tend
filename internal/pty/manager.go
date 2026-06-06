package pty

import (
	"sync"

	"github.com/dusto/tend/api"
)

// Manager owns the daemon's panes: it spawns them, tracks them by id, and reaps
// them on close or shutdown. Panes outlive any visible terminal view, so the
// Manager — not an editor — is their lifecycle owner. It is safe for concurrent
// use.
type Manager struct {
	mu    sync.Mutex
	panes map[api.PaneID]*Pane
}

// NewManager returns an empty Manager.
func NewManager() *Manager {
	return &Manager{panes: make(map[api.PaneID]*Pane)}
}

// Spawn starts a new pane and tracks it. When its process exits on its own, the
// pane stays registered (and readable headless) until Close or Shutdown; callers
// reap it explicitly.
func (m *Manager) Spawn(cfg SpawnConfig) (*Pane, error) {
	p, err := spawn(cfg)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.panes[p.ID] = p
	m.mu.Unlock()
	return p, nil
}

// Get returns the pane with id, if present.
func (m *Manager) Get(id api.PaneID) (*Pane, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.panes[id]
	return p, ok
}

// List returns the tracked panes in unspecified order.
func (m *Manager) List() []*Pane {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Pane, 0, len(m.panes))
	for _, p := range m.panes {
		out = append(out, p)
	}
	return out
}

// Close terminates the pane with id and removes it. It returns false if no such
// pane is tracked.
func (m *Manager) Close(id api.PaneID) bool {
	m.mu.Lock()
	p, ok := m.panes[id]
	if ok {
		delete(m.panes, id)
	}
	m.mu.Unlock()
	if !ok {
		return false
	}
	_ = p.Close()
	return true
}

// Shutdown terminates and removes every pane.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	panes := make([]*Pane, 0, len(m.panes))
	for _, p := range m.panes {
		panes = append(panes, p)
	}
	m.panes = make(map[api.PaneID]*Pane)
	m.mu.Unlock()
	for _, p := range panes {
		_ = p.Close()
	}
}
