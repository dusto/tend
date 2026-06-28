package api

import (
	"encoding/json"
	"time"
)

// EventScope is the kind of logical stream an event belongs to.
type EventScope string

// EventScope values: the logical stream kinds.
const (
	ScopeSession   EventScope = "session"
	ScopeWorkspace EventScope = "workspace" // repo-wide, shared by all worktrees
	ScopeWorktree  EventScope = "worktree"
	ScopePane      EventScope = "pane"
)

// EventKind distinguishes a raw event from a compaction summary record. The two
// share a (stream_id, seq) only across kinds, so dedup is by (stream_id, seq, kind).
type EventKind string

// EventKind values.
const (
	KindEvent   EventKind = "event"
	KindSummary EventKind = "summary"
)

// Event is the envelope every TEND event is delivered in. Ordering and
// contiguity hold within a single stream only; cross-stream correlation uses
// TS and ids, never Seq. See docs: Events.
type Event struct {
	StreamID StreamID   `json:"stream_id"`
	Scope    EventScope `json:"scope"`
	Kind     EventKind  `json:"kind"`
	// Seq is monotonic within the stream; contiguous except across an explicit
	// summary range. For a summary, Seq is the range's from_seq.
	Seq uint64 `json:"seq"`
	// CursorSeq is the value a client stores after processing this record:
	// ==Seq for a normal event, ==to_seq for a summary (advances past the range).
	CursorSeq uint64 `json:"cursor_seq"`
	// Type is the event name, e.g. "agent_message_chunk". See EventDefs.
	Type string `json:"type"`
	// TS is for cross-stream correlation only.
	TS time.Time `json:"ts"`
	// WorktreeRoot is set on events that pertain to a specific worktree (e.g.
	// on the shared workspace stream) so clients can attribute them.
	WorktreeRoot string `json:"worktree_root,omitempty"`
	// Payload is the type-specific body (see the matching EventDef).
	Payload json.RawMessage `json:"payload,omitempty"`
	// Summary carries the compacted range when Kind == KindSummary.
	Summary *SummaryInfo `json:"summary,omitempty"`
}

// SummaryInfo describes the range a summary record subsumes.
type SummaryInfo struct {
	FromSeq uint64 `json:"from_seq"`
	ToSeq   uint64 `json:"to_seq"`
}

// EventDef declares a TEND event type for the generated contract. Payload holds
// a zero value of the payload struct (nil for payloadless events).
type EventDef struct {
	Type    string
	Scope   EventScope
	Payload any
	Summary string
}

// EventDefs is the event catalog (initial set; ACP-shaped core + approvals).
// Further event payloads are added as their features land.
var EventDefs = []EventDef{
	{Type: "agent_message_chunk", Scope: ScopeSession, Payload: AgentMessageChunk{}, Summary: "A streamed chunk of an agent message."},
	{Type: "agent_thought_chunk", Scope: ScopeSession, Payload: AgentThoughtChunk{}, Summary: "A streamed chunk of the agent's reasoning (thinking), distinct from its message."},
	{Type: "tool_call", Scope: ScopeSession, Payload: ToolCall{}, Summary: "An agent tool call started."},
	{Type: "tool_call_update", Scope: ScopeSession, Payload: ToolCallUpdate{}, Summary: "Progress update for a tool call."},
	{Type: "turn_end", Scope: ScopeSession, Payload: TurnEnd{}, Summary: "The agent's turn ended."},
	{Type: "approval_requested", Scope: ScopeSession, Payload: ApprovalRequested{}, Summary: "A mutating action is awaiting approval."},
	{Type: "approval_resolved", Scope: ScopeSession, Payload: ApprovalResolved{}, Summary: "A pending approval was resolved."},
	{Type: "agent_error", Scope: ScopeSession, Payload: AgentError{}, Summary: "A session's turn failed (e.g. its provider process exited mid-turn)."},
	{Type: "provider_started", Scope: ScopeWorkspace, Payload: ProviderStarted{}, Summary: "A provider process joined the pool (spawned for a turn or an explicit start). Repo-wide: delivered on the workspace stream."},
	{Type: "provider_stopped", Scope: ScopeWorkspace, Payload: ProviderStopped{}, Summary: "A provider process left the pool (exit or crash). Repo-wide: delivered on the workspace stream."},
	{Type: "provider_notification", Scope: ScopeSession, Payload: ProviderNotification{}, Summary: "A provider-private ACP notification preserved verbatim as a metadata event."},
	{Type: "agent_mode_updated", Scope: ScopeSession, Payload: AgentModeUpdated{}, Summary: "A session's active mode (reasoning/thought level) changed, by a client set or the agent itself."},
	{Type: "agent_model_updated", Scope: ScopeSession, Payload: AgentModelUpdated{}, Summary: "A session's active model changed."},
	{Type: "task_created", Scope: ScopeWorkspace, Payload: TaskChange{}, Summary: "A task was created. Repo-wide: delivered on the workspace stream."},
	{Type: "task_updated", Scope: ScopeWorkspace, Payload: TaskChange{}, Summary: "A task changed (e.g. claimed or linked). Repo-wide: delivered on the workspace stream."},
	{Type: "task_commented", Scope: ScopeWorkspace, Payload: TaskChange{}, Summary: "A comment was added to a task. Repo-wide: delivered on the workspace stream."},
	{Type: "task_closed", Scope: ScopeWorkspace, Payload: TaskChange{}, Summary: "A task was closed. Repo-wide: delivered on the workspace stream."},
	{Type: "pane_output", Scope: ScopePane, Payload: PaneOutput{}, Summary: "A chunk of a pane's output, on the pane stream. Lossy under load; pane.read is the authoritative scrollback."},
	{Type: "pane_exited", Scope: ScopePane, Payload: PaneExited{}, Summary: "A pane's process exited."},
}

// --- Event payloads (initial set) ---

// AgentMessageChunk is a streamed chunk of an agent message.
type AgentMessageChunk struct {
	SessionID SessionID `json:"session_id"`
	Text      string    `json:"text"`
}

// AgentThoughtChunk is a streamed chunk of the agent's reasoning (its thinking
// output), distinct from the agent's message so a client can render it as a
// separate thinking block.
type AgentThoughtChunk struct {
	SessionID SessionID `json:"session_id"`
	Text      string    `json:"text"`
}

// ToolCall signals that an agent tool call has started.
type ToolCall struct {
	SessionID  SessionID       `json:"session_id"`
	ToolCallID string          `json:"tool_call_id"`
	Name       string          `json:"name"`
	RawInput   json.RawMessage `json:"raw_input,omitempty"`
}

// ToolCallUpdate is a progress update for an in-flight tool call.
type ToolCallUpdate struct {
	SessionID  SessionID `json:"session_id"`
	ToolCallID string    `json:"tool_call_id"`
	Status     string    `json:"status"` // e.g. "in_progress" | "completed" | "failed"
}

// TurnEnd signals that the agent's turn has ended.
type TurnEnd struct {
	SessionID SessionID `json:"session_id"`
}

// ApprovalRequested signals that a mutating action is awaiting approval.
type ApprovalRequested struct {
	SessionID  SessionID  `json:"session_id"`
	ApprovalID ApprovalID `json:"approval_id"`
	Kind       string     `json:"kind"` // "file_edit" | "pane_run" | "code_action" | ...
}

// ApprovalResolved signals that a pending approval was resolved.
type ApprovalResolved struct {
	SessionID  SessionID  `json:"session_id"`
	ApprovalID ApprovalID `json:"approval_id"`
	Approved   bool       `json:"approved"`
	Reason     string     `json:"reason,omitempty"`
}

// AgentError signals that a session's turn failed.
type AgentError struct {
	SessionID SessionID `json:"session_id"`
	Message   string    `json:"message"`
}

// ProviderStarted signals that a provider process joined the pool, either
// spawned to serve a turn or warmed by an explicit provider.start.
type ProviderStarted struct {
	ProviderID ProviderID `json:"provider_id"`
}

// ProviderStopped signals that a provider process left the pool.
type ProviderStopped struct {
	ProviderID ProviderID `json:"provider_id"`
	Reason     string     `json:"reason,omitempty"`
}

// ProviderNotification preserves a provider-private ACP notification that has no
// dedicated TEND event, so nothing the agent reports is silently dropped.
type ProviderNotification struct {
	SessionID SessionID       `json:"session_id"`
	Method    string          `json:"method"`
	Raw       json.RawMessage `json:"raw,omitempty"`
}

// AgentModeUpdated signals that a session's active mode (reasoning/thought
// level) changed — either a client called session.set_mode or the agent
// switched modes itself (ACP current_mode_update).
type AgentModeUpdated struct {
	SessionID     SessionID `json:"session_id"`
	CurrentModeID string    `json:"current_mode_id"`
}

// AgentModelUpdated signals that a session's active model changed.
type AgentModelUpdated struct {
	SessionID      SessionID `json:"session_id"`
	CurrentModelID string    `json:"current_model_id"`
}
