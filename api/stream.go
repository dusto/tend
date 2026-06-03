package api

import "strings"

// Stream-id prefixes for the logical stream taxonomy.
const (
	streamSession   = "session:"
	streamPane      = "pane:"
	streamWorkspace = "workspace:"
	streamWorktree  = "worktree:"
)

// SessionStream is the stream id for a session's events.
func SessionStream(id SessionID) StreamID { return StreamID(streamSession + string(id)) }

// PaneStream is the stream id for a pane's events.
func PaneStream(id PaneID) StreamID { return StreamID(streamPane + string(id)) }

// WorkspaceStream is the stream id for a repo-wide workspace stream. It is shared
// by all worktrees of the repo (task list changes, provider start/stop).
func WorkspaceStream(id WorkspaceID) StreamID { return StreamID(streamWorkspace + string(id)) }

// WorktreeStream is the stream id for worktree-local events. The worktree id is a
// stable short hash of the worktree root, so it is unambiguous within a workspace
// and stable across restarts.
func WorktreeStream(ws WorkspaceID, wt WorktreeID) StreamID {
	return StreamID(streamWorktree + string(ws) + ":" + string(wt))
}

// Scope returns the event scope a stream id belongs to, and false if the id does
// not match a known stream form.
func (s StreamID) Scope() (EventScope, bool) {
	switch {
	case strings.HasPrefix(string(s), streamSession):
		return ScopeSession, true
	case strings.HasPrefix(string(s), streamPane):
		return ScopePane, true
	case strings.HasPrefix(string(s), streamWorktree):
		return ScopeWorktree, true
	case strings.HasPrefix(string(s), streamWorkspace):
		return ScopeWorkspace, true
	}
	return "", false
}
