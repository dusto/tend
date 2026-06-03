package session

import (
	"sync"

	"github.com/dusto/tend/api"
)

// Registry is the daemon's set of live sessions, keyed by session id. It is safe
// for concurrent use.
type Registry struct {
	mu       sync.Mutex
	sessions map[api.SessionID]*Session
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{sessions: make(map[api.SessionID]*Session)}
}

// Create registers a new idle session for id and returns it. It panics if a
// session with that id already exists (ids are daemon-assigned and unique).
func (r *Registry) Create(id api.SessionID, providerID api.ProviderID, task api.TaskRef, worktreeRoot string) *Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.sessions[id]; dup {
		panic("session: duplicate id " + string(id))
	}
	s := &Session{
		ID:           id,
		ProviderID:   providerID,
		Task:         task,
		WorktreeRoot: worktreeRoot,
		Stream:       api.SessionStream(id),
		status:       api.StatusIdle,
	}
	r.sessions[id] = s
	return s
}

// Get returns the session with id, if present.
func (r *Registry) Get(id api.SessionID) (*Session, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	return s, ok
}

// Remove drops a session from the registry.
func (r *Registry) Remove(id api.SessionID) {
	r.mu.Lock()
	delete(r.sessions, id)
	r.mu.Unlock()
}

// List returns the live sessions in unspecified order.
func (r *Registry) List() []*Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		out = append(out, s)
	}
	return out
}
