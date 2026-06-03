package api

// Params and results for the editor-facing agent lifecycle methods. A session is
// task-scoped: it pins a provider process for the task's workspace and runs in a
// specific worktree. Turn progress arrives as events on the session's stream, not
// in these results.

// AgentStartParams opens a task-scoped agent session. The task carries the
// workspace; WorktreeRoot is the directory the session operates in.
type AgentStartParams struct {
	ProviderID   ProviderID `json:"provider_id"`
	Task         TaskRef    `json:"task"`
	WorktreeRoot string     `json:"worktree_root"`
}

// AgentStartResult reports the new session, the stream its events arrive on, and
// its initial status.
type AgentStartResult struct {
	SessionID SessionID     `json:"session_id"`
	StreamID  StreamID      `json:"stream_id"`
	Status    SessionStatus `json:"status"`
}

// AgentPromptParams sends one prompt turn to a session. The call blocks until the
// turn ends; the turn's output streams as events on the session's stream.
type AgentPromptParams struct {
	SessionID SessionID `json:"session_id"`
	Text      string    `json:"text"`
}

// AgentPromptResult reports the turn's stop reason and the session's resulting
// status (idle when the turn completed, error when it failed).
type AgentPromptResult struct {
	StopReason string        `json:"stop_reason"`
	Status     SessionStatus `json:"status"`
}

// AgentCancelParams cancels the in-flight turn on a session, returning it to idle.
type AgentCancelParams struct {
	SessionID SessionID `json:"session_id"`
}

// AgentStopParams ends a session and releases its hold on the provider process.
type AgentStopParams struct {
	SessionID SessionID `json:"session_id"`
}
