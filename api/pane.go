package api

// Pane view states reported by pane.list: whether a visible terminal view is
// currently bridged to the daemon-owned PTY.
const (
	PaneAttached = "attached"
	PaneDetached = "detached"
)

// PaneInfo describes a daemon-owned pane.
type PaneInfo struct {
	PaneID       PaneID      `json:"pane_id"`
	WorkspaceID  WorkspaceID `json:"workspace_id,omitempty"`
	WorktreeRoot string      `json:"worktree_root,omitempty"`
	Cwd          string      `json:"cwd,omitempty"`
	// Running is true while the pane's process is alive; ExitCode is meaningful
	// only once it is false.
	Running  bool `json:"running"`
	ExitCode int  `json:"exit_code,omitempty"`
	// ViewState is "attached" (a visible terminal view is bridged) or "detached"
	// (running headless, no view).
	ViewState string `json:"view_state"`
}

// PaneOpenParams opens an idle shell pane. A pane.open carrying a SessionID is
// agent-initiated and is approval-gated; without one it is a user-initiated open
// and is ungated. Cwd defaults to WorktreeRoot when empty.
type PaneOpenParams struct {
	WorkspaceID  WorkspaceID `json:"workspace_id,omitempty"`
	WorktreeRoot string      `json:"worktree_root,omitempty"`
	Cwd          string      `json:"cwd,omitempty"`
	SessionID    SessionID   `json:"session_id,omitempty"`
}

// PaneListParams lists panes, optionally filtered to one workspace.
type PaneListParams struct {
	WorkspaceID WorkspaceID `json:"workspace_id,omitempty"`
}

// PaneListResult is the set of panes.
type PaneListResult struct {
	Panes []PaneInfo `json:"panes"`
}

// PaneReadParams reads a pane's captured output. Tail, when > 0, returns only the
// last Tail bytes.
type PaneReadParams struct {
	PaneID PaneID `json:"pane_id"`
	Tail   int    `json:"tail,omitempty"`
}

// PaneReadResult is a pane's captured output (base64-encoded as JSON bytes, so it
// is safe for arbitrary terminal bytes).
type PaneReadResult struct {
	PaneID  PaneID `json:"pane_id"`
	Data    []byte `json:"data"`
	Running bool   `json:"running"`
}

// PaneCloseParams closes a pane.
type PaneCloseParams struct {
	PaneID PaneID `json:"pane_id"`
}

// PaneCloseResult acknowledges a close.
type PaneCloseResult struct{}

// PaneRunParams runs a command in a pane. It is task-bound (SessionID is
// required) and approval-gated.
type PaneRunParams struct {
	PaneID    PaneID    `json:"pane_id"`
	Command   string    `json:"command"`
	SessionID SessionID `json:"session_id"`
}

// PaneRunResult acknowledges that a run was accepted (the command was approved
// and sent to the pane); output arrives on the pane's event stream.
type PaneRunResult struct{}

// PaneOutput is a chunk of a pane's output, delivered on the pane stream. Data
// is JSON bytes (base64), so arbitrary terminal output is safe.
type PaneOutput struct {
	PaneID PaneID `json:"pane_id"`
	Data   []byte `json:"data"`
}

// PaneExited signals that a pane's process has exited.
type PaneExited struct {
	PaneID   PaneID `json:"pane_id"`
	ExitCode int    `json:"exit_code"`
}
