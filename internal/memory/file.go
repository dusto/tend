package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path"
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
			Apply:   h.e.Apply,
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

// Steering returns the steering entries that apply to path, in id order: always
// entries always, glob entries when a glob matches path, manual entries never.
// An empty path yields only always-steering.
func (p *FileProvider) Steering(_ context.Context, path string) ([]api.MemoryEntry, error) {
	entries, order, err := p.index()
	if err != nil {
		return nil, err
	}
	var out []api.MemoryEntry
	for _, id := range order {
		e := entries[id]
		if e.Kind == api.MemoryKindSteering && steeringApplies(e, path) {
			out = append(out, e)
		}
	}
	return out, nil
}

// steeringApplies reports whether a steering entry activates for the context
// path. An unknown apply mode is treated as always, matching the parse-time
// default for steering.
func steeringApplies(e api.MemoryEntry, path string) bool {
	switch e.Apply {
	case api.MemoryApplyManual:
		return false
	case api.MemoryApplyGlob:
		if path == "" {
			return false
		}
		for _, g := range e.Globs {
			if globMatch(g, path) {
				return true
			}
		}
		return false
	default: // always
		return true
	}
}

// Write creates or overwrites a memory file (an upsert keyed by id) and returns
// the stored entry. It renders YAML frontmatter + the markdown body and writes
// atomically (temp file + rename) so a concurrent Search/Get never reads a
// half-written file. The directory is created on demand. The next index call
// picks up the change via the directory signature.
func (p *FileProvider) Write(_ context.Context, in api.MemoryWriteParams) (api.MemoryEntry, error) {
	id, err := resolveID(in)
	if err != nil {
		return api.MemoryEntry{}, err
	}
	kind := firstNonEmpty(in.Kind, api.MemoryKindNote)
	apply, globs := normalizeSteering(kind, in.Apply, in.Globs)
	e := api.MemoryEntry{
		ID:          id,
		WorkspaceID: p.ws,
		Kind:        kind,
		Apply:       apply,
		Globs:       globs,
		Title:       in.Title,
		Tags:        in.Tags,
		Task:        p.taskRef(taskString(in.Task)),
		Text:        strings.TrimSpace(in.Text),
		CreatedAt:   time.Now().UTC(),
	}
	if err := writeFileAtomic(filepath.Join(p.dir, string(id)+".md"), renderMemory(e)); err != nil {
		return api.MemoryEntry{}, err
	}
	return e, nil
}

// resolveID picks the file id: an explicit id, else a slug of the title, else a
// generated id when the title is empty too. An explicit id must be a safe single
// filename segment: since memory.write is not approval-gated, a caller id like
// "../../README" would otherwise escape the memory directory. Derived and
// generated ids are safe by construction, so only the explicit id is checked.
func resolveID(in api.MemoryWriteParams) (api.MemoryID, error) {
	if in.ID != "" {
		if !safeID(string(in.ID)) {
			return "", fmt.Errorf("%w: %q", ErrInvalidID, in.ID)
		}
		return in.ID, nil
	}
	if slug := slugify(in.Title); slug != "" {
		return api.MemoryID(slug), nil
	}
	return api.MemoryID("mem-" + randomSuffix()), nil
}

// safeID reports whether id is a safe single filename segment: a non-empty string
// of [A-Za-z0-9._-] with no ".." run, so it cannot contain a path separator,
// name an absolute path, or traverse out of the memory directory.
func safeID(id string) bool {
	if id == "" || id == "." || id == ".." || strings.Contains(id, "..") {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// ErrInvalidID reports that an explicit memory id is not a safe filename segment
// (e.g. it contains a path separator or "..").
var ErrInvalidID = fmt.Errorf("memory: invalid id")

// renderMemory serializes an entry to a markdown file: YAML frontmatter fenced by
// --- lines, then the body. Kind is omitted for the default note.
func renderMemory(e api.MemoryEntry) []byte {
	out := frontmatterOut{
		ID:      string(e.ID),
		Title:   e.Title,
		Tags:    e.Tags,
		Task:    taskString(e.Task),
		Created: e.CreatedAt.Format(time.RFC3339),
	}
	if e.Kind != "" && e.Kind != api.MemoryKindNote {
		out.Kind = e.Kind
	}
	// Activation is meaningful only for steering; the default (always) is left
	// implicit so authored note/steering files stay clean.
	if e.Kind == api.MemoryKindSteering && e.Apply != "" && e.Apply != api.MemoryApplyAlways {
		out.Apply = e.Apply
		out.Globs = e.Globs
	}
	// yaml.Marshal never fails for this flat struct; ignore its error.
	meta, _ := yaml.Marshal(out)

	var b strings.Builder
	b.WriteString("---\n")
	b.Write(meta)
	b.WriteString("---\n")
	if e.Text != "" {
		b.WriteString("\n")
		b.WriteString(e.Text)
		b.WriteString("\n")
	}
	return []byte(b.String())
}

// frontmatterOut is the write-side view of frontmatter: it omits empty fields so
// authored files stay clean (mirrors the read-side frontmatter struct).
type frontmatterOut struct {
	ID      string   `yaml:"id"`
	Kind    string   `yaml:"kind,omitempty"`
	Apply   string   `yaml:"apply,omitempty"`
	Globs   []string `yaml:"globs,omitempty"`
	Title   string   `yaml:"title,omitempty"`
	Tags    []string `yaml:"tags,omitempty"`
	Task    string   `yaml:"task,omitempty"`
	Created string   `yaml:"created"`
}

// taskString renders a TaskRef back to its frontmatter "provider:id" form (or
// "id" with no provider), the inverse of taskRef. It returns "" for a nil ref.
func taskString(t *api.TaskRef) string {
	if t == nil {
		return ""
	}
	if t.Provider != "" {
		return t.Provider + ":" + t.ID
	}
	return t.ID
}

// slugify turns a title into a filesystem-safe id: lowercase, non-alphanumeric
// runs collapsed to single hyphens, trimmed, capped in length.
func slugify(s string) string {
	var b strings.Builder
	prevHyphen := false
	for _, r := range strings.ToLower(s) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevHyphen = false
		default:
			if !prevHyphen && b.Len() > 0 {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
		if b.Len() >= slugMaxLen {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

// slugMaxLen caps a title-derived id so a long title cannot produce an unwieldy
// filename.
const slugMaxLen = 60

// globMatch reports whether a doublestar glob matches a slash-separated path. It
// supports "**" (matching zero or more path segments), plus "*" and "?" within a
// segment (via path.Match). Both inputs are normalized to forward slashes, so
// authored patterns like "**/*.go" or "src/**/test_*.go" work cross-platform.
func globMatch(pattern, name string) bool {
	pat := strings.Split(strings.Trim(filepath.ToSlash(pattern), "/"), "/")
	seg := strings.Split(strings.Trim(filepath.ToSlash(name), "/"), "/")
	return matchSegments(pat, seg)
}

// matchSegments matches pattern segments against path segments, treating "**" as
// a wildcard over zero or more whole segments.
func matchSegments(pat, seg []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			// A trailing ** matches any remaining segments (including none).
			if len(pat) == 1 {
				return true
			}
			// Otherwise try to match the rest of the pattern at every suffix.
			for i := 0; i <= len(seg); i++ {
				if matchSegments(pat[1:], seg[i:]) {
					return true
				}
			}
			return false
		}
		if len(seg) == 0 {
			return false
		}
		// path.Match's * and ? do not cross "/", which is what we want per segment.
		if ok, err := path.Match(pat[0], seg[0]); err != nil || !ok {
			return false
		}
		pat, seg = pat[1:], seg[1:]
	}
	return len(seg) == 0
}

// randomSuffix is a short random hex string used to make an id unique when a
// write carries neither an id nor a title.
func randomSuffix() string {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand should not fail; fall back to a time-based suffix.
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}

// writeFileAtomic writes data to path via a temp file in the same directory then
// a rename, so a reader never observes a partially written file. It creates the
// parent directory on demand.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".memory-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// On any error after creation, don't leave the temp file behind. After a
	// successful rename the temp no longer exists, so the Remove is a harmless no-op.
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
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

	kind := firstNonEmpty(meta.Kind, api.MemoryKindNote)
	apply, globs := normalizeSteering(kind, meta.Apply, meta.Globs)
	e := api.MemoryEntry{
		ID:          api.MemoryID(firstNonEmpty(meta.ID, baseName(path))),
		WorkspaceID: p.ws,
		Kind:        kind,
		Apply:       apply,
		Globs:       globs,
		Title:       meta.Title,
		Tags:        meta.Tags,
		Task:        p.taskRef(meta.Task),
		Text:        strings.TrimSpace(string(body)),
		CreatedAt:   meta.created(fileModTime(path)),
	}
	return e, nil
}

// normalizeSteering resolves the activation mode for an entry. Activation is
// meaningful only for steering: notes get none. A steering entry with no mode
// defaults to always; globs are kept only in glob mode.
func normalizeSteering(kind, apply string, globs []string) (string, []string) {
	if kind != api.MemoryKindSteering {
		return "", nil
	}
	if apply == "" {
		apply = api.MemoryApplyAlways
	}
	if apply != api.MemoryApplyGlob {
		return apply, nil
	}
	return apply, globs
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
	Apply   string   `yaml:"apply"`
	Globs   []string `yaml:"globs"`
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
