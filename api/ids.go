package api

// Identifier types used across the wire contract. They are string types for
// clarity at call sites; each serializes as a plain JSON string.
type (
	// WorkspaceID is the canonical realpath of the repo's common git dir, or
	// "ephemeral:<canonical-path>" for a non-git, read-only workspace.
	WorkspaceID string
	// WorktreeID is a stable short hash of the canonical worktree root.
	WorktreeID string
	// SessionID identifies an agent session.
	SessionID string
	// PaneID identifies a daemon-owned pane/PTY.
	PaneID string
	// ProviderID identifies an ACP provider definition.
	ProviderID string
	// ApprovalID identifies one pending approval.
	ApprovalID string
	// ChangeSetID identifies a (proposed or applied) file change set.
	ChangeSetID string
	// ClientID identifies one connected client (editor or observer).
	ClientID string
	// DaemonEpoch is fresh per daemon process; it keys client cursor stores and
	// invalidates stale per-stream sequence numbers across daemon restarts.
	DaemonEpoch string
)

// StreamID identifies one logical event stream. Forms:
//
//	session:<session_id>
//	workspace:<workspace_id>
//	worktree:<workspace_id>:<worktree_id>
//	pane:<pane_id>
type StreamID string

// TaskRef identifies a task in a provider-agnostic way. A task is repo-wide
// (keyed by WorkspaceID); a session also carries a worktree (see agent.start).
type TaskRef struct {
	Provider    string      `json:"provider"` // "beads" | "kata" | ...
	WorkspaceID WorkspaceID `json:"workspace_id"`
	ID          string      `json:"id"`
}

// WorkspaceInfo describes an opened workspace.
type WorkspaceInfo struct {
	WorkspaceID  WorkspaceID `json:"workspace_id"`
	WorktreeRoot string      `json:"worktree_root"`
	WorktreeID   WorktreeID  `json:"worktree_id"`
	Ephemeral    bool        `json:"ephemeral"` // read-only, outside git
	DaemonEpoch  DaemonEpoch `json:"daemon_epoch"`
}
