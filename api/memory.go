package api

import "time"

// Memory kinds (MemoryEntry.Kind). Memory has a type dimension: an episodic note
// the agent authored, versus standing steering (rules/standards/instructions)
// that guides the agent. They are retrieved differently — notes by search,
// steering additionally by activation rules — so the kind is first-class.
const (
	// MemoryKindNote is an episodic, agent-authored working note (the default).
	MemoryKindNote = "note"
	// MemoryKindSteering is standing guidance: rules, standards, or instructions,
	// including memory imported from other agents' instruction files.
	MemoryKindSteering = "steering"
)

// Steering activation modes (MemoryEntry.Apply): how a steering memory is applied
// to agent context, not just searched. The model mirrors Kiro inclusion modes
// (always/fileMatch/manual) and Cursor rules (alwaysApply/globs) so one model
// spans tools. Notes carry no activation mode.
const (
	// MemoryApplyAlways always includes the steering (the default for steering).
	MemoryApplyAlways = "always"
	// MemoryApplyGlob includes the steering when a context path matches one of its
	// globs (like Kiro fileMatch / Cursor globs).
	MemoryApplyGlob = "glob"
	// MemoryApplyManual never auto-includes the steering; it surfaces only through
	// memory.search / memory.get.
	MemoryApplyManual = "manual"
)

// MemoryEntry is one stored memory: a task-bound, agent-authored note, or a piece
// of standing steering (see Kind). The backend is pluggable; the default stores
// each entry as a markdown file with YAML frontmatter.
type MemoryEntry struct {
	ID          MemoryID    `json:"id"`
	WorkspaceID WorkspaceID `json:"workspace_id"`
	// Kind is the memory type (note or steering); empty is treated as note.
	Kind string `json:"kind,omitempty"`
	// Apply is the steering activation mode (always|glob|manual); empty on notes,
	// and defaults to always on steering. See MemoryApply*.
	Apply string `json:"apply,omitempty"`
	// Globs are the match patterns for Apply == glob (doublestar globs, e.g.
	// "**/*.go"), matched against a context path relative to the worktree root.
	Globs []string `json:"globs,omitempty"`
	// Task is the task the note was written under, or nil for a workspace-level
	// note.
	Task  *TaskRef `json:"task,omitempty"`
	Title string   `json:"title,omitempty"`
	Tags  []string `json:"tags,omitempty"`
	// Text is the note body (markdown).
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at,omitzero"`
}

// MemoryHit is one memory.search result: identity and a short snippet rather than
// the full body, so a search stays concise. Fetch the full text with memory.get.
type MemoryHit struct {
	ID    MemoryID `json:"id"`
	Kind  string   `json:"kind,omitempty"`
	Apply string   `json:"apply,omitempty"`
	Title string   `json:"title,omitempty"`
	Tags  []string `json:"tags,omitempty"`
	Task  *TaskRef `json:"task,omitempty"`
	// Snippet is a short excerpt of the body around the match.
	Snippet string `json:"snippet"`
}

// MemorySearchParams searches a workspace's memories.
type MemorySearchParams struct {
	WorkspaceID WorkspaceID `json:"workspace_id"`
	// Query is the search text; its terms are matched against each memory's title,
	// tags, and body.
	Query string `json:"query"`
	// Kind, when set, restricts the search to memories of that kind (note or
	// steering); empty searches all kinds.
	Kind string `json:"kind,omitempty"`
	// Limit caps the number of hits; 0 uses a sensible default.
	Limit int `json:"limit,omitempty"`
}

// MemorySearchResult is the ranked hits, most relevant first.
type MemorySearchResult struct {
	Hits []MemoryHit `json:"hits"`
}

// MemoryGetParams fetches one memory entry's full text by id within a workspace.
type MemoryGetParams struct {
	WorkspaceID WorkspaceID `json:"workspace_id"`
	ID          MemoryID    `json:"id"`
}

// MemoryGetResult is the full memory entry.
type MemoryGetResult struct {
	Entry MemoryEntry `json:"entry"`
}

// MemoryWriteParams creates or updates one memory entry (an upsert keyed by id).
// A memory is agent-authored and task-bound; the write is not approval-gated.
type MemoryWriteParams struct {
	WorkspaceID WorkspaceID `json:"workspace_id"`
	// ID addresses an existing entry to overwrite; empty derives a stable id from
	// the title, or a generated id when the title is also empty.
	ID MemoryID `json:"id,omitempty"`
	// Kind is the memory type (note or steering); empty defaults to note.
	Kind string `json:"kind,omitempty"`
	// Apply is the steering activation mode (always|glob|manual); ignored for
	// notes, and defaults to always for steering. See MemoryApply*.
	Apply string `json:"apply,omitempty"`
	// Globs are the match patterns for Apply == glob.
	Globs []string `json:"globs,omitempty"`
	Title string   `json:"title,omitempty"`
	Tags  []string `json:"tags,omitempty"`
	// Task binds the note to the task it was written under, or nil for a
	// workspace-level note.
	Task *TaskRef `json:"task,omitempty"`
	// Text is the note body (markdown).
	Text string `json:"text"`
}

// MemoryWriteResult is the stored entry, with its resolved id and timestamp.
type MemoryWriteResult struct {
	Entry MemoryEntry `json:"entry"`
}

// MemorySteeringParams resolves the steering that applies to a context within a
// workspace. Path is an optional worktree-relative file path: steering with
// apply=glob is included when one of its globs matches Path, apply=always is
// always included, and apply=manual is never auto-included. With an empty Path
// only always-steering applies.
type MemorySteeringParams struct {
	WorkspaceID WorkspaceID `json:"workspace_id"`
	Path        string      `json:"path,omitempty"`
}

// MemorySteeringResult is the full steering entries that apply, ordered by id, so
// a client can inject their bodies into agent context.
type MemorySteeringResult struct {
	Entries []MemoryEntry `json:"entries"`
}
