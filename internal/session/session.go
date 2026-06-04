// Package session holds the daemon's authoritative session state. A session is
// workspace-bound and outlives any client: the daemon tracks its identifiers,
// lifecycle status, and the interaction it is currently blocked on. Editor
// binding and client identity are layered on separately.
package session

import (
	"fmt"
	"sync"

	"github.com/dusto/tend/api"
)

// Pending is the interaction a waiting session is blocked on: an approval or a
// clarification, identified by its id.
type Pending struct {
	Kind api.PendingKind
	ID   string
}

// Session is the daemon's authoritative state for one agent session.
type Session struct {
	ID           api.SessionID
	ProviderID   api.ProviderID
	Task         api.TaskRef
	WorktreeRoot string
	// Stream is the session's event stream id, stable across process restarts.
	Stream api.StreamID

	mu      sync.Mutex
	status  api.SessionStatus
	pending *Pending
	// Editor binding: owner is the client currently serving editor-local calls
	// ("" when headless); expectedEditor is the editor identity auto-bind matches,
	// recorded when the binding is claimed and retained across disconnects so the
	// same editor reattaches without a re-claim.
	owner          api.ClientID
	expectedEditor api.ClientID
}

// allowed lists the legal status transitions. ended is terminal.
var allowed = map[api.SessionStatus]map[api.SessionStatus]bool{
	api.StatusIdle: {
		api.StatusRunning: true, api.StatusError: true, api.StatusEnded: true,
	},
	api.StatusRunning: {
		api.StatusIdle: true, api.StatusWaitingApproval: true, api.StatusWaitingClarification: true,
		api.StatusError: true, api.StatusEnded: true,
	},
	api.StatusWaitingApproval: {
		api.StatusRunning: true, api.StatusIdle: true, api.StatusError: true, api.StatusEnded: true,
	},
	api.StatusWaitingClarification: {
		api.StatusRunning: true, api.StatusIdle: true, api.StatusError: true, api.StatusEnded: true,
	},
	api.StatusError: {
		api.StatusIdle: true, api.StatusEnded: true,
	},
	api.StatusEnded: {},
}

// Status returns the current lifecycle status.
func (s *Session) Status() api.SessionStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// Pending returns the interaction the session is blocked on, if any.
func (s *Session) Pending() (Pending, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		return Pending{}, false
	}
	return *s.pending, true
}

// SetStatus transitions the session to status to, enforcing the lifecycle.
// Entering waiting_approval or waiting_clarification requires a matching pending
// interaction; every other status clears any pending interaction.
func (s *Session) SetStatus(to api.SessionStatus, pending *Pending) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !allowed[s.status][to] {
		return fmt.Errorf("session %s: illegal transition %s -> %s", s.ID, s.status, to)
	}
	want, waiting := pendingKindFor(to)
	if waiting {
		if pending == nil {
			return fmt.Errorf("session %s: %s requires a pending interaction", s.ID, to)
		}
		if pending.Kind != want {
			return fmt.Errorf("session %s: %s requires a %s, got %s", s.ID, to, want, pending.Kind)
		}
		if pending.ID == "" {
			return fmt.Errorf("session %s: %s requires a pending id", s.ID, to)
		}
		p := *pending
		s.pending = &p
	} else {
		s.pending = nil
	}
	s.status = to
	return nil
}

// Owner returns the session's editor-binding owner and whether one is bound.
func (s *Session) Owner() (api.ClientID, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.owner, s.owner != ""
}

// ExpectedEditor returns the editor identity auto-bind matches, or "" if none
// has been recorded yet.
func (s *Session) ExpectedEditor() api.ClientID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.expectedEditor
}

// BindOwner sets client as the editor-binding owner, taking over from any
// existing owner, and records it as the expected editor identity. This is the
// deliberate-claim path (manual takeover or initial bind).
func (s *Session) BindOwner(client api.ClientID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.owner = client
	s.expectedEditor = client
}

// AutoBindOwner binds client only if the session is headless and client matches
// the recorded expected editor identity. It reports whether it bound, so a
// non-matching editor leaves the session headless rather than capturing it.
func (s *Session) AutoBindOwner(client api.ClientID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner != "" || s.expectedEditor == "" || client != s.expectedEditor {
		return false
	}
	s.owner = client
	return true
}

// ReleaseOwner clears the binding only if client is the current owner, leaving
// the session headless. The expected editor identity is retained so the same
// editor can reattach. It reports whether it released.
func (s *Session) ReleaseOwner(client api.ClientID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner == "" || s.owner != client {
		return false
	}
	s.owner = ""
	return true
}

// pendingKindFor reports the pending kind a waiting status requires, and whether
// the status is a waiting status at all.
func pendingKindFor(status api.SessionStatus) (api.PendingKind, bool) {
	switch status {
	case api.StatusWaitingApproval:
		return api.PendingApproval, true
	case api.StatusWaitingClarification:
		return api.PendingClarification, true
	}
	return "", false
}
