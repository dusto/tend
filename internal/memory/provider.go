package memory

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/dusto/tend/api"
)

// Provider is one workspace's memory backend behind the memory.* methods. It is
// the swappable seam: the default reads markdown files, but a vector-store or
// external memory engine can replace it without touching the wire contract.
type Provider interface {
	// Search returns the memories matching query, most relevant first, capped at
	// limit (<= 0 uses a default). A non-empty kind restricts results to that
	// memory kind. Hits carry ids + snippets, not full bodies.
	Search(ctx context.Context, query, kind string, limit int) ([]api.MemoryHit, error)
	// Get returns one memory's full entry by id.
	Get(ctx context.Context, id api.MemoryID) (api.MemoryEntry, error)
	// Write creates or updates a memory entry (an upsert keyed by id) and returns
	// the stored entry with its resolved id and timestamp.
	Write(ctx context.Context, params api.MemoryWriteParams) (api.MemoryEntry, error)
}

// Factory builds the memory Provider for a workspace. Each workspace gets one
// provider; the daemon caches them.
type Factory func(api.WorkspaceID) Provider

// InRepoFactory resolves each workspace to its in-tree memory directory
// (<repo>/.tend/memory). It is the default backend; a config-driven factory (a
// central memory repo, or per-repo rules like task sources) can replace it.
func InRepoFactory(ws api.WorkspaceID) Provider {
	return NewFileProvider(ws, inRepoDir(ws))
}

// inRepoDir is the workspace repo's memory directory. The WorkspaceID is the
// canonical common git dir, so a trailing ".git" names the worktree root.
func inRepoDir(ws api.WorkspaceID) string {
	p := strings.TrimPrefix(string(ws), "ephemeral:")
	if filepath.Base(p) == ".git" {
		p = filepath.Dir(p)
	}
	return filepath.Join(p, ".tend", "memory")
}
