package api

// Params and results for the editor-facing agent lifecycle methods. A session is
// task-scoped: it pins a provider process for the task's workspace and runs in a
// specific worktree. Turn progress arrives as events on the session's stream, not
// in these results.

// AgentStartParams opens an agent session. WorktreeRoot is the directory the
// session operates in. Task is OPTIONAL: a task-less session is for
// conversation (a task is assigned later by delegation); work — file/pane
// mutation — still requires a task. WorkspaceID is the session's workspace; it
// may be empty when Task is set (it then defaults to the task's workspace), but
// is required for a task-less session.
type AgentStartParams struct {
	ProviderID   ProviderID  `json:"provider_id"`
	WorkspaceID  WorkspaceID `json:"workspace_id,omitempty"`
	Task         TaskRef     `json:"task,omitzero"`
	WorktreeRoot string      `json:"worktree_root"`
}

// AgentStartResult reports the new session, the stream its events arrive on, and
// its initial status.
type AgentStartResult struct {
	SessionID SessionID     `json:"session_id"`
	StreamID  StreamID      `json:"stream_id"`
	Status    SessionStatus `json:"status"`
}

// Prompt content block types (the ACP content a turn can carry).
const (
	PromptContentText         = "text"
	PromptContentResourceLink = "resource_link"
	PromptContentImage        = "image"
	PromptContentAudio        = "audio"
)

// PromptContentBlock is one content block in a prompt turn: the editor attaches
// structured context (a referenced file, a pasted image) alongside the prompt
// text. Type selects the variant and only that variant's fields are read. It
// mirrors the ACP prompt content the daemon forwards to the provider.
type PromptContentBlock struct {
	// Type is one of the PromptContent* constants.
	Type string `json:"type"`
	// Text is the block text (type "text").
	Text string `json:"text,omitempty"`
	// URI locates the resource: a file:// URL for a resource_link, or the source
	// URI of an image/audio blob (types "resource_link", "image", "audio").
	URI string `json:"uri,omitempty"`
	// Name is a display name for a resource_link (type "resource_link").
	Name string `json:"name,omitempty"`
	// MimeType is the media type of an image/audio blob (types "image", "audio").
	MimeType string `json:"mime_type,omitempty"`
	// Data is the base64-encoded bytes of an image/audio blob (types "image",
	// "audio").
	Data string `json:"data,omitempty"`
}

// AgentPromptParams sends one prompt turn to a session. The call blocks until the
// turn ends; the turn's output streams as events on the session's stream. A turn
// carries either plain Text or a Content block array (structured ACP content: the
// prompt text plus attached files/images/audio); when Content is non-empty it
// supersedes Text.
type AgentPromptParams struct {
	SessionID SessionID            `json:"session_id"`
	Text      string               `json:"text,omitempty"`
	Content   []PromptContentBlock `json:"content,omitempty"`
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
