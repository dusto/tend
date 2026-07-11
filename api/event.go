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

// ContextSummary is the payload of a summary record (Type "summary",
// Kind KindSummary): the condensed text standing in for the raw turns in the
// subsumed range. The range itself is on the envelope (Event.Summary); this
// carries the session and the summary text a client renders in place of the
// collapsed turns.
type ContextSummary struct {
	SessionID SessionID `json:"session_id"`
	// Text is the condensed summary of the compacted turns.
	Text string `json:"text"`
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
	{Type: "user_prompt", Scope: ScopeSession, Payload: UserPrompt{}, Summary: "The user's prompt content for a turn, emitted as the turn starts so a replay/resume sees the human side of the conversation (the session stream otherwise carries only agent output). Text only; attachment blob content is not persisted, just counted."},
	{Type: "agent_prompt_usage", Scope: ScopeSession, Payload: AgentPromptUsage{}, Summary: "The size of the prompt input the daemon composed for a turn: bytes and an approximate, model-agnostic token estimate. Measures only client-side prompt content, not the agent-owned system prompt/history."},
	{Type: "agent_context_usage", Scope: ScopeSession, Payload: AgentContextUsage{}, Summary: "The agent's context-window fullness (tokens used of the window size), from the provider's usage_update. The authoritative signal for context-window management (e.g. compaction triggering), distinct from a single turn's spend. Carries cost when the provider reports it."},
	{Type: "agent_token_usage", Scope: ScopeSession, Payload: AgentTokenUsage{}, Summary: "The provider's authoritative token accounting for one completed turn, from the session/prompt result (input/output/cached/reasoning/total). Supersedes the agent_prompt_usage estimate; includes a per-model breakdown when the provider supplies one."},
	{Type: "approval_requested", Scope: ScopeSession, Payload: ApprovalRequested{}, Summary: "A mutating action is awaiting approval."},
	{Type: "approval_resolved", Scope: ScopeSession, Payload: ApprovalResolved{}, Summary: "A pending approval was resolved."},
	{Type: "agent_error", Scope: ScopeSession, Payload: AgentError{}, Summary: "A session's turn failed (e.g. its provider process exited mid-turn)."},
	{Type: "provider_started", Scope: ScopeWorkspace, Payload: ProviderStarted{}, Summary: "A provider process joined the pool (spawned for a turn or an explicit start). Repo-wide: delivered on the workspace stream."},
	{Type: "provider_stopped", Scope: ScopeWorkspace, Payload: ProviderStopped{}, Summary: "A provider process left the pool (exit or crash). Repo-wide: delivered on the workspace stream."},
	{Type: "provider_notification", Scope: ScopeSession, Payload: ProviderNotification{}, Summary: "A provider-private ACP notification preserved verbatim as a metadata event."},
	{Type: "agent_mode_updated", Scope: ScopeSession, Payload: AgentModeUpdated{}, Summary: "A session's active mode (behavior/permission mode) changed, by a client set or the agent itself."},
	{Type: "agent_model_updated", Scope: ScopeSession, Payload: AgentModelUpdated{}, Summary: "A session's active model changed."},
	{Type: "agent_thought_level_updated", Scope: ScopeSession, Payload: AgentThoughtLevelUpdated{}, Summary: "A session's active reasoning/thought level changed."},
	{Type: "agent_plan", Scope: ScopeSession, Payload: AgentPlan{}, Summary: "The agent's tactical per-turn plan (its todo list): the full set of entries with their status, replacing any prior plan for the turn."},
	{Type: "slash_commands_updated", Scope: ScopeSession, Payload: SlashCommandsUpdated{}, Summary: "A session's merged slash-command set changed (the agent advertised new commands): the full set of provider + daemon commands, replacing the prior set."},
	{Type: "task_created", Scope: ScopeWorkspace, Payload: TaskChange{}, Summary: "A task was created. Repo-wide: delivered on the workspace stream."},
	{Type: "task_updated", Scope: ScopeWorkspace, Payload: TaskChange{}, Summary: "A task changed (e.g. claimed or linked). Repo-wide: delivered on the workspace stream."},
	{Type: "task_commented", Scope: ScopeWorkspace, Payload: TaskChange{}, Summary: "A comment was added to a task. Repo-wide: delivered on the workspace stream."},
	{Type: "task_closed", Scope: ScopeWorkspace, Payload: TaskChange{}, Summary: "A task was closed. Repo-wide: delivered on the workspace stream."},
	{Type: "pane_output", Scope: ScopePane, Payload: PaneOutput{}, Summary: "A chunk of a pane's output, on the pane stream. Lossy under load; pane.read is the authoritative scrollback."},
	{Type: "pane_exited", Scope: ScopePane, Payload: PaneExited{}, Summary: "A pane's process exited."},
	{Type: "memory_written", Scope: ScopeWorkspace, Payload: MemoryWritten{}, Summary: "A memory entry was created or updated (memory.write). Repo-wide: delivered on the workspace stream."},
	{Type: "memory_searched", Scope: ScopeWorkspace, Payload: MemorySearched{}, Summary: "A workspace's memories were searched (memory.search), so a supervisor can see what an agent recalled. Repo-wide: delivered on the workspace stream."},
	{Type: "summary", Scope: ScopeSession, Payload: ContextSummary{}, Summary: "A compaction record standing in for a range [from_seq,to_seq] of raw session turns: the condensed text a client renders in place of the collapsed turns. It is a kind=summary record (Event.summary carries the range), served on replay as a range replacement rather than appended live."},
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

// UserPrompt is the user's prompt content for one turn, emitted on the session
// stream as the turn starts so a replay or resume sees the user's side of the
// conversation — the stream otherwise carries only the agent's output. The
// daemon composes Text from the turn's prompt (the plain text, or the
// concatenated text blocks). Attachment blocks (resource links, images, audio)
// are counted in Attachments but their content is deliberately NOT persisted to
// the durable log: their bytes (base64 blobs) are not the request text a replay
// needs, and keeping them out bounds the log. So this persists the user's text,
// nothing more.
type UserPrompt struct {
	SessionID SessionID `json:"session_id"`
	// Text is the composed text prompt content for the turn (may be empty when the
	// turn carried only attachments).
	Text string `json:"text"`
	// Attachments is the number of non-text blocks in the turn whose content is not
	// persisted here.
	Attachments int `json:"attachments,omitempty"`
}

// AgentPromptUsage reports the size of the prompt input the daemon composed for
// one turn (measured as the turn starts, before the provider send, so it is
// reported even if the send then fails). It measures only client-side prompt
// content: thick ACP agents own their system prompt, tool definitions, and
// history, which the daemon cannot see or measure. Only text blocks contribute
// to the byte and token counts; other blocks (resource links, images, audio) are
// counted as attachments, since their content is not composed as prompt text.
type AgentPromptUsage struct {
	SessionID SessionID `json:"session_id"`
	// TextBytes is the UTF-8 byte length of the text prompt content composed.
	TextBytes int `json:"text_bytes"`
	// TextChars is the rune count of that text.
	TextChars int `json:"text_chars"`
	// TokensApprox is an approximate, model-agnostic token estimate of the text
	// (no per-model tokenizer). Approximate is always true to flag this.
	TokensApprox int `json:"tokens_approx"`
	// Blocks is the number of content blocks in the turn.
	Blocks int `json:"blocks"`
	// Attachments is the number of non-text blocks whose content is not counted
	// toward the token estimate.
	Attachments int `json:"attachments"`
	// Approximate flags that TokensApprox is a heuristic, not a provider-reported
	// count. It is always true.
	Approximate bool `json:"approximate"`
}

// UsageCost is a monetary cost a provider attaches to usage (e.g. Claude reports
// a cumulative session cost). Amount is in Currency's units.
type UsageCost struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

// AgentContextUsage reports how full the agent's context window is for a session,
// from the provider's usage_update notification. Codex and Claude both emit the
// same {used, size} shape, so it is provider-agnostic. This is the authoritative
// signal for context-window management — a compaction trigger watches
// UsedTokens/WindowTokens — as opposed to a single turn's spend (AgentTokenUsage).
// It is cumulative and updates through a session as context grows.
type AgentContextUsage struct {
	SessionID SessionID `json:"session_id"`
	// UsedTokens is how many tokens of the window are currently in use.
	UsedTokens int `json:"used_tokens"`
	// WindowTokens is the context window size in tokens (0 if the provider omits
	// it).
	WindowTokens int `json:"window_tokens"`
	// Cost is the session's cumulative cost when the provider reports it (Claude);
	// nil when unreported.
	Cost *UsageCost `json:"cost,omitempty"`
}

// AgentTokenUsage reports the provider's authoritative token accounting for one
// completed turn, taken from the session/prompt result. It supersedes the
// daemon's agent_prompt_usage estimate (which measures only client-composed input
// and cannot see the agent-owned system prompt/history). Fields a provider does
// not report are zero. ModelUsage carries the per-model breakdown when the
// provider supplies one (e.g. Codex _meta.quota.model_usage).
type AgentTokenUsage struct {
	SessionID SessionID `json:"session_id"`
	// InputTokens and OutputTokens are the turn's non-cached input and generated
	// output tokens.
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	// CachedReadTokens and CachedWriteTokens are prompt-cache read/write tokens when
	// the provider distinguishes them (Claude reports both; Codex reports reads).
	CachedReadTokens  int `json:"cached_read_tokens,omitempty"`
	CachedWriteTokens int `json:"cached_write_tokens,omitempty"`
	// ReasoningTokens is reasoning/thinking output tokens when reported separately
	// (Codex thoughtTokens/reasoningOutputTokens).
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
	// TotalTokens is the provider's own total for the turn.
	TotalTokens int `json:"total_tokens"`
	// ModelUsage is the per-model breakdown when the provider supplies one; nil
	// otherwise.
	ModelUsage []ModelTokenUsage `json:"model_usage,omitempty"`
}

// ModelTokenUsage is one model's share of a turn's token usage, for providers
// that route a turn across models and report per-model counts.
type ModelTokenUsage struct {
	Model            string `json:"model"`
	InputTokens      int    `json:"input_tokens,omitempty"`
	OutputTokens     int    `json:"output_tokens,omitempty"`
	CachedReadTokens int    `json:"cached_read_tokens,omitempty"`
	ReasoningTokens  int    `json:"reasoning_tokens,omitempty"`
	TotalTokens      int    `json:"total_tokens,omitempty"`
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

// AgentThoughtLevelUpdated signals that a session's active reasoning/thought
// level changed, by a client set (session.set_thought_level) or the agent itself
// (a config_option_update).
type AgentThoughtLevelUpdated struct {
	SessionID             SessionID `json:"session_id"`
	CurrentThoughtLevelID string    `json:"current_thought_level_id"`
}

// AgentPlan is the agent's tactical per-turn plan (its todo list). Each plan
// update from the agent carries the full set of entries, replacing any prior
// plan for the turn rather than appending — a client renders Entries as the
// current plan.
type AgentPlan struct {
	SessionID SessionID   `json:"session_id"`
	Entries   []PlanEntry `json:"entries"`
}

// SlashCommandsUpdated signals that a session's merged slash-command set changed,
// carrying the full set (provider + daemon commands) so a client replaces rather
// than merges. Emitted when the agent advertises commands (ACP
// available_commands_update); the daemon commands are constant within the set.
type SlashCommandsUpdated struct {
	SessionID SessionID      `json:"session_id"`
	Commands  []SlashCommand `json:"commands"`
}

// PlanEntry is one item in an agent plan: what it intends to do, how it ranks it,
// and how far along it is. Priority and Status carry the ACP plan vocabulary
// (priority: high|medium|low; status: pending|in_progress|completed) passed
// through verbatim so a client need not know the daemon's provider mapping.
type PlanEntry struct {
	Content  string `json:"content"`
	Priority string `json:"priority,omitempty"`
	Status   string `json:"status"`
}

// MemoryWritten signals that a memory entry was created or updated via
// memory.write. It carries identity, not the full body: fetch that with
// memory.get. Repo-wide, so it is delivered on the workspace stream.
type MemoryWritten struct {
	WorkspaceID WorkspaceID `json:"workspace_id"`
	ID          MemoryID    `json:"id"`
	Kind        string      `json:"kind,omitempty"`
	Title       string      `json:"title,omitempty"`
	Task        *TaskRef    `json:"task,omitempty"`
}

// MemorySearched signals that a workspace's memories were searched, so a
// supervisor can observe what an agent recalled. It carries the query and the
// number of hits, not the results. Repo-wide, on the workspace stream.
type MemorySearched struct {
	WorkspaceID WorkspaceID `json:"workspace_id"`
	Query       string      `json:"query"`
	// Kind is the kind filter the search applied, if any.
	Kind string `json:"kind,omitempty"`
	// Results is how many hits the search returned.
	Results int `json:"results"`
}
