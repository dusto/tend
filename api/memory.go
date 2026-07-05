package api

import "time"

// MemoryEntry is one stored memory note: a task-bound, agent-authored note the
// daemon can search and retrieve later. The backend is pluggable; the default
// stores each entry as a markdown file with YAML frontmatter.
type MemoryEntry struct {
	ID          MemoryID    `json:"id"`
	WorkspaceID WorkspaceID `json:"workspace_id"`
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
