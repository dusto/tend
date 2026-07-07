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
	// Provenance is set on entries produced by importing an external file, so a
	// re-import can update them in place without clobbering later human edits.
	Provenance *MemoryProvenance `json:"provenance,omitempty"`
}

// MemoryProvenance records that an entry was produced by importing an external
// agent's memory/instruction file (see the memory import adapters). It lets a
// re-import find the entry it wrote, detect whether a human has since edited it,
// and update in place rather than duplicating or clobbering.
type MemoryProvenance struct {
	// Source is the importer/adapter that produced the entry (e.g. "kiro",
	// "agents").
	Source string `json:"source"`
	// Origin is the source file the entry was imported from, relative to the repo
	// root.
	Origin string `json:"origin"`
	// Hash is the content hash of the imported body at import time. On re-import, a
	// stored entry whose current body no longer hashes to this was edited by a
	// human, so the import skips it instead of overwriting.
	Hash string `json:"hash"`
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
	// Provenance, when set, marks the entry as imported from an external file (see
	// MemoryProvenance); it is persisted so a re-import can update in place.
	Provenance *MemoryProvenance `json:"provenance,omitempty"`
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

// MemoryContextParams assembles the memory context that applies to a task/session
// and condenses it to a character budget, so a caller injects a bounded digest
// rather than every full entry. It combines the activated steering (always, plus
// glob-matched to Path — the memory.steering semantics) with, optionally, the
// notes matching Query, then reduces the whole to Budget via the daemon's
// configured summarizer. TEND owns this context, so it reduces what it injects
// rather than dumping everything every turn.
type MemoryContextParams struct {
	WorkspaceID WorkspaceID `json:"workspace_id"`
	// Path is an optional worktree-relative file path for steering glob activation
	// (same semantics as memory.steering). Empty includes only always-steering.
	Path string `json:"path,omitempty"`
	// Query, when set, also includes the notes matching it (via memory.search),
	// so task-relevant notes join the steering in the assembled context.
	Query string `json:"query,omitempty"`
	// Limit caps how many matching notes are included; 0 uses a sensible default.
	// Ignored when Query is empty.
	Limit int `json:"limit,omitempty"`
	// Budget is the target character budget for the assembled context; 0 uses the
	// summarizer's configured default. Content within budget is returned verbatim;
	// only an over-budget assembly is condensed.
	Budget int `json:"budget,omitempty"`
}

// MemoryContextResult is the assembled, budget-bounded context to inject.
type MemoryContextResult struct {
	// Text is the assembled context, condensed to the budget when it was too large.
	Text string `json:"text"`
	// Included are the ids of the entries that fed the context, in assembly order
	// (steering first, then notes), for observability — even when Text was then
	// summarized into a single digest.
	Included []MemoryID `json:"included,omitempty"`
	// Summarized reports whether the assembly was condensed by the summarizer
	// (false when it already fit the budget and is returned verbatim).
	Summarized bool `json:"summarized"`
}
