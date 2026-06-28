package api

// SessionStatus is an agent session's lifecycle state, as tracked by the daemon
// and reflected to attached clients.
type SessionStatus string

// SessionStatus values.
const (
	// StatusIdle: the session exists with no in-flight turn.
	StatusIdle SessionStatus = "idle"
	// StatusRunning: a turn is in flight.
	StatusRunning SessionStatus = "running"
	// StatusWaitingApproval: a turn is blocked on a pending approval.
	StatusWaitingApproval SessionStatus = "waiting_approval"
	// StatusWaitingClarification: a turn is blocked on a pending clarification.
	StatusWaitingClarification SessionStatus = "waiting_clarification"
	// StatusError: the session's last turn failed.
	StatusError SessionStatus = "error"
	// StatusEnded: the session is terminated (terminal).
	StatusEnded SessionStatus = "ended"
)

// PendingKind is the kind of interaction a waiting session is blocked on.
type PendingKind string

// PendingKind values.
const (
	PendingApproval      PendingKind = "approval"
	PendingClarification PendingKind = "clarification"
)

// SessionPending is the interaction a waiting session is blocked on, surfaced
// on a SessionInfo so a client can show what a session needs without a separate
// approval.list call.
type SessionPending struct {
	Kind PendingKind `json:"kind"`
	ID   string      `json:"id"`
}

// SessionMode is one selectable agent mode advertised by the provider at
// session/new. Modes are the provider's reasoning/behavior control (clients
// surface them as the reasoning/thought level); the set is empty when the
// provider offers no choice.
type SessionMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// SessionModel is one selectable model advertised by the provider at
// session/new; the set is empty when the provider offers no choice.
type SessionModel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// SessionInfo is the listed view of one daemon session: enough to render a
// session overview (status, the task it works on, where it runs) and to route
// to it (id + stream), without a follow-up call. Pending is set only when the
// session is waiting on an approval or clarification.
type SessionInfo struct {
	SessionID  SessionID  `json:"session_id"`
	ProviderID ProviderID `json:"provider_id"`
	// WorkspaceID is the workspace the session is bound to. It is always set,
	// independent of Task, so a task-less (conversation) session still carries a
	// workspace identity for clients to render and route by.
	WorkspaceID WorkspaceID `json:"workspace_id"`
	// Task is the bound task, or nil for a task-less (conversation) session.
	Task         *TaskRef        `json:"task,omitempty"`
	WorktreeRoot string          `json:"worktree_root"`
	StreamID     StreamID        `json:"stream_id"`
	Status       SessionStatus   `json:"status"`
	Pending      *SessionPending `json:"pending,omitempty"`
	// EditorBound is true when this client (the caller) currently holds the
	// session's editor binding, so editor-local tools route to it.
	EditorBound bool `json:"editor_bound"`
	// CurrentModeID and AvailableModes describe the session's provider modes
	// (reasoning/thought level); both are empty when the provider offers none.
	CurrentModeID  string        `json:"current_mode_id,omitempty"`
	AvailableModes []SessionMode `json:"available_modes,omitempty"`
	// CurrentModelID and AvailableModels describe the session's provider models;
	// both are empty when the provider offers no choice.
	CurrentModelID  string         `json:"current_model_id,omitempty"`
	AvailableModels []SessionModel `json:"available_models,omitempty"`
}

// SessionSetModeParams sets a session's active mode (reasoning/thought level).
type SessionSetModeParams struct {
	SessionID SessionID `json:"session_id"`
	ModeID    string    `json:"mode_id"`
}

// SessionSetModeResult reports the session's new active mode after the change.
type SessionSetModeResult struct {
	SessionID     SessionID `json:"session_id"`
	CurrentModeID string    `json:"current_mode_id"`
}

// SessionSetModelParams sets a session's active model.
type SessionSetModelParams struct {
	SessionID SessionID `json:"session_id"`
	ModelID   string    `json:"model_id"`
}

// SessionSetModelResult reports the session's new active model after the change.
type SessionSetModelResult struct {
	SessionID      SessionID `json:"session_id"`
	CurrentModelID string    `json:"current_model_id"`
}

// SessionListParams lists the daemon's sessions. WorkspaceID, when set, filters
// to one workspace; empty lists all.
type SessionListParams struct {
	WorkspaceID WorkspaceID `json:"workspace_id,omitempty"`
}

// SessionListResult is the set of sessions, ordered by session id for a stable
// listing.
type SessionListResult struct {
	Sessions []SessionInfo `json:"sessions"`
}

// SessionClaimParams moves a session's editor binding to the calling client, so
// editor-local calls for that session route to it. The caller must be an
// editor-capable client. This is the deliberate focus-switch path when an
// editor drives several sessions.
type SessionClaimParams struct {
	SessionID SessionID `json:"session_id"`
}

// SessionClaimResult returns the session's updated listed view after the claim.
type SessionClaimResult struct {
	Session SessionInfo `json:"session"`
}
