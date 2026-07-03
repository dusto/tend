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
	ID         api.SessionID
	ProviderID api.ProviderID
	// WorkspaceID is the session's workspace (the ACP pool key + list filter),
	// set independently of Task so a task-less session still belongs to a
	// workspace.
	WorkspaceID api.WorkspaceID
	// Task is the work the session is bound to, or the zero TaskRef when the
	// session is task-less (created for conversation; a task is assigned later
	// by delegation). HasTask reports which.
	Task         api.TaskRef
	WorktreeRoot string
	// Stream is the session's event stream id, stable across process restarts.
	Stream api.StreamID

	mu      sync.Mutex
	status  api.SessionStatus
	pending *Pending
	// Provider mode/model selection, captured from session/new and updated on
	// set_mode/set_model or the agent's own current_mode_update. The available
	// lists are empty when the provider offers no choice.
	currentModeID          string
	availableModes         []api.SessionMode
	currentModelID         string
	availableModels        []api.SessionModel
	currentThoughtLevelID  string
	availableThoughtLevels []api.SessionThoughtLevel
	// providerCommands are the agent's advertised slash commands, captured from
	// the ACP available_commands_update. Empty until the agent advertises any.
	providerCommands []api.SlashCommand
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

// HasTask reports whether a task is bound to the session. Work (file/pane
// mutation) requires one; a task-less session can converse and read but not
// mutate until a task is assigned. Task is immutable after Create, so this
// needs no lock.
func (s *Session) HasTask() bool {
	return s.Task.ID != ""
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

// SetModes records the provider's advertised modes and the active one, captured
// at session start. A nil/empty list means the provider offers no modes.
func (s *Session) SetModes(current string, available []api.SessionMode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentModeID = current
	s.availableModes = available
}

// SetModels records the provider's advertised models and the active one.
func (s *Session) SetModels(current string, available []api.SessionModel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentModelID = current
	s.availableModels = available
}

// SetCurrentMode updates only the active mode (after a set_mode or an agent-side
// current_mode_update), leaving the available list unchanged.
func (s *Session) SetCurrentMode(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentModeID = id
}

// SetCurrentModel updates only the active model.
func (s *Session) SetCurrentModel(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentModelID = id
}

// SetThoughtLevels records the provider's advertised thought levels and the
// active one, captured from session/new.
func (s *Session) SetThoughtLevels(current string, available []api.SessionThoughtLevel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentThoughtLevelID = current
	s.availableThoughtLevels = available
}

// SetCurrentThoughtLevel updates only the active thought level.
func (s *Session) SetCurrentThoughtLevel(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentThoughtLevelID = id
}

// Modes returns the active mode id and the available modes.
func (s *Session) Modes() (string, []api.SessionMode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentModeID, s.availableModes
}

// Models returns the active model id and the available models.
func (s *Session) Models() (string, []api.SessionModel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentModelID, s.availableModels
}

// ThoughtLevels returns the active thought level id and the available levels.
func (s *Session) ThoughtLevels() (string, []api.SessionThoughtLevel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentThoughtLevelID, s.availableThoughtLevels
}

// SetProviderCommands records the agent's advertised slash commands, replacing
// any prior set (each available_commands_update carries the full list).
func (s *Session) SetProviderCommands(cmds []api.SlashCommand) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.providerCommands = cmds
}

// ProviderCommands returns the agent's advertised slash commands.
func (s *Session) ProviderCommands() []api.SlashCommand {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.providerCommands
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
