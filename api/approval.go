package api

import (
	"encoding/json"
	"time"
)

// Approval kinds: the operation an approval gates.
const (
	ApprovalFileEdit   = "file_edit"
	ApprovalPaneRun    = "pane_run"
	ApprovalCodeAction = "code_action"
)

// ApprovalDetail is the decision context for a pending approval: everything about
// the operation a client needs to review and decide without an editor preview. It
// carries the operation kind and exactly one matching body. The gate-owned
// envelope — approval id, created/expiry timestamps — is delivered alongside this
// detail (see the approval gate's pending state and the prompt/list methods), so
// it is not duplicated here.
type ApprovalDetail struct {
	Kind string `json:"kind"`

	FileEdit   *FileEditApproval   `json:"file_edit,omitempty"`
	PaneRun    *PaneRunApproval    `json:"pane_run,omitempty"`
	CodeAction *CodeActionApproval `json:"code_action,omitempty"`
}

// FileEditApproval is the decision context for a file mutation: the change-set id
// and the per-target diffs and bases. The diff and base let a non-editor client
// review the exact proposed change and the revision it was computed against.
type FileEditApproval struct {
	ChangeSetID ChangeSetID      `json:"change_set_id"`
	Targets     []FileEditTarget `json:"targets"`
}

// FileEditTarget is one file in a file-edit approval.
type FileEditTarget struct {
	URI string `json:"uri"`
	// Base is the revision the edit was computed against (changedtick for an open
	// buffer, content hash for a closed file).
	Base FileBase `json:"base"`
	// Diff is a unified diff of the current content against the proposed content.
	Diff string `json:"diff"`
}

// PaneRunApproval is the decision context for running a command in a pane: the
// exact command, working directory, and an environment summary, plus the target
// pane.
type PaneRunApproval struct {
	PaneID  PaneID   `json:"pane_id"`
	Command string   `json:"command"`
	Cwd     string   `json:"cwd"`
	Env     []string `json:"env,omitempty"`
}

// CodeActionApproval is the decision context for a mutating code action: its
// title and the file it targets. The resulting edits flow through the file-edit
// path, so the change itself is reviewed as a file_edit.
type CodeActionApproval struct {
	Title string `json:"title"`
	URI   string `json:"uri"`
}

// --- approval.list / approval.respond (plugin -> daemon) ---

// ApprovalListParams lists pending approvals. SessionID, when set, filters to one
// session; empty lists all.
type ApprovalListParams struct {
	SessionID SessionID `json:"session_id,omitempty"`
}

// ApprovalListResult is the set of pending approvals, each a self-contained
// payload (envelope + detail).
type ApprovalListResult struct {
	Approvals []ApprovalSummary `json:"approvals"`
}

// ApprovalSummary is one pending approval as delivered to a client: the
// gate-owned envelope (id, session, kind, expiry) plus the decision Detail
// (api.ApprovalDetail).
type ApprovalSummary struct {
	ApprovalID ApprovalID      `json:"approval_id"`
	SessionID  SessionID       `json:"session_id"`
	Kind       string          `json:"kind"`
	ExpiresAt  time.Time       `json:"expires_at,omitzero"`
	Detail     json.RawMessage `json:"detail,omitempty"`
}

// ApprovalRespondParams resolves a pending approval. Only prompt-capable clients
// may call it.
type ApprovalRespondParams struct {
	ApprovalID ApprovalID `json:"approval_id"`
	Approved   bool       `json:"approved"`
	Reason     string     `json:"reason,omitempty"`
}

// ApprovalRespondResult is the (empty) acknowledgement that a respond was
// accepted. It is a request, not a notification: the caller must be able to
// observe capability and validity errors. The resolution itself is broadcast to
// clients as an approval_resolved event.
type ApprovalRespondResult struct{}
