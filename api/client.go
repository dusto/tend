package api

import (
	"encoding/json"
	"time"
)

// Params types for daemon->attached-client methods (notifications; no result).

// EventPushParams delivers one event on a subscribed stream.
type EventPushParams struct {
	Event Event `json:"event"`
}

// PromptRaiseParams raises a clarification prompt to a session's attached
// clients. Approvals are not raised here: they broadcast on the workspace stream
// as approval_requested, with approval.list as the durable snapshot.
type PromptRaiseParams struct {
	SessionID SessionID `json:"session_id"`
	Kind      string    `json:"kind"` // "clarification"
	// ApprovalID is retained for the shared prompt envelope; unused for
	// clarification prompts.
	ApprovalID ApprovalID `json:"approval_id,omitempty"`
	// Prompt is human-facing text; Detail carries structured context for the
	// prompt kind.
	Prompt string          `json:"prompt"`
	Detail json.RawMessage `json:"detail,omitempty"`
	// ExpiresAt is the prompt's deadline, if any.
	ExpiresAt time.Time `json:"expires_at,omitzero"`
}

// SubscriptionClosedParams signals that one stream subscription was dropped.
type SubscriptionClosedParams struct {
	StreamID StreamID `json:"stream_id"`
	Reason   string   `json:"reason"`
	// LastSentSeq is diagnostic only; clients resume from their own processed
	// last_seq, never from this value.
	LastSentSeq *uint64 `json:"last_sent_seq,omitempty"`
}
