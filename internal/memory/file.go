package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/dusto/tend/api"
)

// defaultLimit caps memory.search results when the caller passes none.
const defaultLimit = 20

// snippetRadius is how much body text a search snippet shows around the first
// match (characters before, and after).
const (
	snippetBefore = 40
	snippetAfter  = 180
)

// FileProvider is the default memory backend: each memory is a markdown file with
// YAML frontmatter under a directory. It parses the frontmatter into an in-memory
// index so search and get query structured entries rather than grepping files,
// and reloads only when the directory changes. It is safe for concurrent use.
type FileProvider struct {
	ws  api.WorkspaceID
	dir string

	mu    sync.Mutex
	cache map[api.MemoryID]api.MemoryEntry
	order []api.MemoryID // stable index order (by id) for deterministic ranking
	sig   string         // directory signature; a change triggers a reload
}

// NewFileProvider returns a memory provider reading markdown files under dir for
// workspace ws. A missing directory is not an error: it reads as empty.
func NewFileProvider(ws api.WorkspaceID, dir string) *FileProvider {
	return &FileProvider{ws: ws, dir: dir}
}

// Search returns memories whose title, tags, or body match the query, ranked by
// match strength (title/tag matches weigh more than body), then most recent.
func (p *FileProvider) Search(_ context.Context, query, kind string, limit int) ([]api.MemoryHit, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	terms := terms(query)
	if len(terms) == 0 {
		return nil, nil
	}
	entries, order, err := p.index()
	if err != nil {
		return nil, err
	}

	type scored struct {
		e     api.MemoryEntry
		score int
	}
	var hits []scored
	for _, id := range order {
		e := entries[id]
		if kind != "" && e.Kind != kind {
			continue
		}
		if s := score(e, terms); s > 0 {
			hits = append(hits, scored{e: e, score: s})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].e.CreatedAt.After(hits[j].e.CreatedAt)
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]api.MemoryHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, api.MemoryHit{
			ID:      h.e.ID,
			Kind:    h.e.Kind,
			Title:   h.e.Title,
			Tags:    h.e.Tags,
			Task:    h.e.Task,
			Snippet: snippet(h.e.Text, terms),
		})
	}
	return out, nil
}

// Get returns the full entry for id, or ErrNotFound.
func (p *FileProvider) Get(_ context.Context, id api.MemoryID) (api.MemoryEntry, error) {
	entries, _, err := p.index()
	if err != nil {
		return api.MemoryEntry{}, err
	}
	if e, ok := entries[id]; ok {
		return e, nil
	}
	return api.MemoryEntry{}, ErrNotFound
}

// ErrNotFound reports that a memory id does not exist in the workspace.
var ErrNotFound = fmt.Errorf("memory: no such entry")

// index returns the parsed entries, reloading from disk only when the directory's
// signature (file set + mtimes) has changed since the last read.
func (p *FileProvider) index() (map[api.MemoryID]api.MemoryEntry, []api.MemoryID, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	sig, files, err := scan(p.dir)
	if err != nil {
		return nil, nil, err
	}
	if p.cache != nil && sig == p.sig {
		return p.cache, p.order, nil
	}

	cache := make(map[api.MemoryID]api.MemoryEntry, len(files))
	order := make([]api.MemoryID, 0, len(files))
	for _, f := range files {
		e, err := p.parseFile(f)
		if err != nil {
			continue // a malformed file is skipped, not fatal for the whole index
		}
		if _, dup := cache[e.ID]; !dup {
			order = append(order, e.ID)
		}
		cache[e.ID] = e
	}
	slices.Sort(order)
	p.cache, p.order, p.sig = cache, order, sig
	return cache, order, nil
}

// scan lists the markdown files under dir and computes a signature that changes
// when a file is added, removed, or modified. A missing directory yields no files.
func scan(dir string) (string, []string, error) {
	var files []string
	var b strings.Builder
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".md") {
			return nil
		}
		// Include the file only if it can be stat'd; one that vanished mid-walk is
		// skipped rather than aborting the whole scan.
		if info, ierr := d.Info(); ierr == nil {
			files = append(files, path)
			fmt.Fprintf(&b, "%s:%d:%d\n", path, info.Size(), info.ModTime().UnixNano())
		}
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	return b.String(), files, nil
}

// parseFile reads one memory file: YAML frontmatter (between --- fences) followed
// by the markdown body.
func (p *FileProvider) parseFile(path string) (api.MemoryEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return api.MemoryEntry{}, err
	}
	fm, body := splitFrontmatter(data)
	var meta frontmatter
	if len(fm) > 0 {
		if err := yaml.Unmarshal(fm, &meta); err != nil {
			return api.MemoryEntry{}, err
		}
	}

	e := api.MemoryEntry{
		ID:          api.MemoryID(firstNonEmpty(meta.ID, baseName(path))),
		WorkspaceID: p.ws,
		Kind:        firstNonEmpty(meta.Kind, api.MemoryKindNote),
		Title:       meta.Title,
		Tags:        meta.Tags,
		Task:        p.taskRef(meta.Task),
		Text:        strings.TrimSpace(string(body)),
		CreatedAt:   meta.created(fileModTime(path)),
	}
	return e, nil
}

// taskRef parses a frontmatter task value ("provider:id" or "id") into a TaskRef
// bound to the provider's workspace, or nil when empty.
func (p *FileProvider) taskRef(s string) *api.TaskRef {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	provider, id := "", s
	if before, after, ok := strings.Cut(s, ":"); ok {
		provider, id = before, after
	}
	return &api.TaskRef{Provider: provider, WorkspaceID: p.ws, ID: id}
}

// frontmatter is the recognized YAML metadata of a memory file. Unknown keys are
// ignored, so files can carry extra fields.
type frontmatter struct {
	ID      string   `yaml:"id"`
	Kind    string   `yaml:"kind"`
	Title   string   `yaml:"title"`
	Tags    []string `yaml:"tags"`
	Task    string   `yaml:"task"`
	Created string   `yaml:"created"`
}

// created parses the frontmatter timestamp (RFC3339 or a plain date), falling
// back to the file's modification time.
func (f frontmatter) created(fallback time.Time) time.Time {
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, f.Created); err == nil {
			return t
		}
	}
	return fallback
}

// splitFrontmatter separates a leading YAML frontmatter block (fenced by --- on
// its own line) from the body. With no frontmatter it returns nil and the whole
// input as body.
func splitFrontmatter(data []byte) (fm, body []byte) {
	s := string(data)
	var rest string
	switch {
	case strings.HasPrefix(s, "---\n"):
		rest = s[len("---\n"):]
	case strings.HasPrefix(s, "---\r\n"):
		rest = s[len("---\r\n"):]
	default:
		return nil, data
	}
	// The closing fence is --- on its own line.
	for _, fence := range []string{"\n---\n", "\n---\r\n"} {
		if before, after, ok := strings.Cut(rest, fence); ok {
			return []byte(before), []byte(after)
		}
	}
	if before, ok := strings.CutSuffix(rest, "\n---"); ok {
		return []byte(before), nil
	}
	return nil, data
}

func baseName(path string) string {
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

func fileModTime(path string) time.Time {
	if info, err := os.Stat(path); err == nil {
		return info.ModTime()
	}
	return time.Time{}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
