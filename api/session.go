package api

import "time"

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
// session/new. Modes are the provider's behavior control (e.g. a permission
// mode); the set is empty when the provider offers no choice. Reasoning effort
// is a separate axis, SessionThoughtLevel.
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

// SessionThoughtLevel is one selectable reasoning/thought level advertised by
// the provider at session/new (a distinct axis from mode); the set is empty when
// the provider offers no choice.
type SessionThoughtLevel struct {
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
	// (behavior/permission mode); both are empty when the provider offers none.
	CurrentModeID  string        `json:"current_mode_id,omitempty"`
	AvailableModes []SessionMode `json:"available_modes,omitempty"`
	// CurrentModelID and AvailableModels describe the session's provider models;
	// both are empty when the provider offers no choice.
	CurrentModelID  string         `json:"current_model_id,omitempty"`
	AvailableModels []SessionModel `json:"available_models,omitempty"`
	// CurrentThoughtLevelID and AvailableThoughtLevels describe the session's
	// reasoning/thought level; both are empty when the provider offers no choice.
	CurrentThoughtLevelID  string                `json:"current_thought_level_id,omitempty"`
	AvailableThoughtLevels []SessionThoughtLevel `json:"available_thought_levels,omitempty"`
	// ResourceUsage is the latest sample of the OS resources the session's agent
	// process is using, or nil when the daemon has no sample (no live process yet,
	// or the platform does not support sampling).
	ResourceUsage *SessionResourceUsage `json:"resource_usage,omitempty"`
}

// SessionResourceUsage samples the OS resources the session's agent process is
// using. It is approximate and best-effort: the daemon samples only the ACP
// agent process it owns (not the agent-spawned children or pty panes). CPUPercent
// is 0 on the first sample of a process, before an interval exists to rate over.
type SessionResourceUsage struct {
	// CPUPercent is the process CPU usage over the last sample interval, as a
	// percentage of one core (may exceed 100 across threads).
	CPUPercent float64 `json:"cpu_percent"`
	// RSSBytes is the process resident set size in bytes.
	RSSBytes int64 `json:"rss_bytes"`
	// SampledAt is when the sample was taken.
	SampledAt time.Time `json:"sampled_at"`
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

// SessionSetThoughtLevelParams sets a session's active reasoning/thought level.
type SessionSetThoughtLevelParams struct {
	SessionID      SessionID `json:"session_id"`
	ThoughtLevelID string    `json:"thought_level_id"`
}

// SessionSetThoughtLevelResult reports the session's new active thought level.
type SessionSetThoughtLevelResult struct {
	SessionID             SessionID `json:"session_id"`
	CurrentThoughtLevelID string    `json:"current_thought_level_id"`
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

// SessionResumeSeedParams reconstructs the context needed to resume work from a
// prior session, so a fresh session can pick up where it left off. The daemon
// renders the prior session's durable event history — the w1h.9 summary records
// standing in for compacted turn ranges, plus the recent raw transcript — and
// combines it with the workspace's applicable memory (steering, and optional
// query-matched notes), then condenses the whole to a character budget. Because
// it reads the durable log (not live provider state) and composes context
// daemon-side, resume works across providers and across a daemon restart, rather
// than depending on a provider-side session load.
//
// Scope: the rendered history is both sides of the conversation — the user's
// per-turn prompts (user_prompt events) and the agent's output (messages,
// thoughts, tool calls, turn boundaries) — condensed with memory into a lossy
// briefing, not a verbatim replay. A session recorded before user_prompt events
// existed carries only the agent's side; pass Query to reintroduce the goal from
// task-bound notes in that case.
type SessionResumeSeedParams struct {
	// SessionID is the prior session whose history seeds the resume. Its event
	// stream is read from the durable log, so the session need not still be live
	// (this is the cross-restart path).
	SessionID SessionID `json:"session_id"`
	// WorkspaceID is the workspace whose memory (steering + optional notes) joins
	// the seed. Required, since the prior session's workspace is not re-derived
	// from the log.
	WorkspaceID WorkspaceID `json:"workspace_id"`
	// Path is an optional worktree-relative path for steering glob activation
	// (same semantics as memory.context/steering). Empty includes only
	// always-steering.
	Path string `json:"path,omitempty"`
	// Query, when set, includes matching notes in the memory portion of the seed
	// (via the memory context assembly).
	Query string `json:"query,omitempty"`
	// Budget is the target character budget for the whole seed; 0 uses the
	// summarizer's configured default. A seed within budget is returned verbatim.
	Budget int `json:"budget,omitempty"`
}

// SessionResumeSeedResult is the assembled resume seed: the opening-prompt
// content for a fresh session that continues the prior session's work.
type SessionResumeSeedResult struct {
	// Text is the resume seed to send as the first turn of a new session. It is
	// empty when the prior session has no readable history and no memory applies.
	Text string `json:"text"`
	// Summarized reports whether the seed was condensed to fit the budget, rather
	// than returned at its full assembled size — so a caller knows the seed is a
	// lossy digest, not a verbatim replay of prior context.
	Summarized bool `json:"summarized"`
	// SourceSessionID echoes the prior session the seed was reconstructed from.
	SourceSessionID SessionID `json:"source_session_id"`
}
