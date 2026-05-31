package api

import "encoding/json"

// Params types for daemon->attached-client methods (notifications; no result).

// EventPushParams delivers one event on a subscribed stream.
type EventPushParams struct {
	Event Event `json:"event"`
}

// PromptRaiseParams raises an approval or clarification to attached clients.
type PromptRaiseParams struct {
	SessionID SessionID `json:"session_id"`
	Kind      string    `json:"kind"` // "approval" | "clarification"
	// ApprovalID is set when Kind == "approval".
	ApprovalID ApprovalID `json:"approval_id,omitempty"`
	// Prompt is human-facing text. Detail carries decision context (a diff,
	// command + cwd, code-action target, base hashes, expiry) for approvals.
	Prompt string          `json:"prompt"`
	Detail json.RawMessage `json:"detail,omitempty"`
}

// SubscriptionClosedParams signals that one stream subscription was dropped.
type SubscriptionClosedParams struct {
	StreamID StreamID `json:"stream_id"`
	Reason   string   `json:"reason"`
	// LastSentSeq is diagnostic only; clients resume from their own processed
	// last_seq, never from this value.
	LastSentSeq *uint64 `json:"last_sent_seq,omitempty"`
}
