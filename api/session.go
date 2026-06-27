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

// SessionInfo is the listed view of one daemon session: enough to render a
// session overview (status, the task it works on, where it runs) and to route
// to it (id + stream), without a follow-up call. Pending is set only when the
// session is waiting on an approval or clarification.
type SessionInfo struct {
	SessionID  SessionID  `json:"session_id"`
	ProviderID ProviderID `json:"provider_id"`
	// Task is the bound task, or nil for a task-less (conversation) session.
	Task         *TaskRef        `json:"task,omitempty"`
	WorktreeRoot string          `json:"worktree_root"`
	StreamID     StreamID        `json:"stream_id"`
	Status       SessionStatus   `json:"status"`
	Pending      *SessionPending `json:"pending,omitempty"`
	// EditorBound is true when this client (the caller) currently holds the
	// session's editor binding, so editor-local tools route to it.
	EditorBound bool `json:"editor_bound"`
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
