package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dusto/tend/api"
)

func writeMemory(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write memory: %v", err)
	}
}

func newProvider(t *testing.T) (*FileProvider, string) {
	t.Helper()
	dir := t.TempDir()
	return NewFileProvider("ws1", dir), dir
}

func TestFileProviderSearchRanksTitleOverBody(t *testing.T) {
	p, dir := newProvider(t)
	writeMemory(t, dir, "a.md", "---\nid: a\ntitle: Deploy runbook\n---\nunrelated body text")
	writeMemory(t, dir, "b.md", "---\nid: b\ntitle: Misc notes\n---\nthe deploy happens after tests")

	hits, err := p.Search(context.Background(), "deploy", "", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(hits))
	}
	// The title match (a) outranks the body-only match (b).
	if hits[0].ID != "a" || hits[1].ID != "b" {
		t.Errorf("order = %s,%s, want a,b", hits[0].ID, hits[1].ID)
	}
}

func TestFileProviderSearchIsConciseWithSnippet(t *testing.T) {
	p, dir := newProvider(t)
	writeMemory(t, dir, "run.md", "---\nid: run\ntitle: Release steps\ntags: [ops]\n---\nRun make release then push the tags to origin.")

	hits, err := p.Search(context.Background(), "release", "", 0)
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits = %+v, err = %v", hits, err)
	}
	h := hits[0]
	if h.ID != "run" || h.Title != "Release steps" || len(h.Tags) != 1 {
		t.Errorf("hit metadata = %+v", h)
	}
	// The snippet carries an excerpt around the match, not the whole store.
	if !strings.Contains(strings.ToLower(h.Snippet), "release") {
		t.Errorf("snippet = %q, want it to contain the match", h.Snippet)
	}
}

func TestFileProviderGetReturnsFullEntry(t *testing.T) {
	p, dir := newProvider(t)
	writeMemory(t, dir, "note.md", "---\nid: n1\ntitle: A note\ntags: [x, y]\ntask: beads:tend-1\ncreated: 2026-06-01T10:00:00Z\n---\nThe full body of the note.")

	e, err := p.Get(context.Background(), "n1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if e.Title != "A note" || e.Text != "The full body of the note." || len(e.Tags) != 2 {
		t.Errorf("entry = %+v", e)
	}
	if e.Task == nil || e.Task.Provider != "beads" || e.Task.ID != "tend-1" || e.Task.WorkspaceID != "ws1" {
		t.Errorf("task = %+v", e.Task)
	}
	if e.CreatedAt.Year() != 2026 || e.CreatedAt.Month() != 6 {
		t.Errorf("created = %v, want 2026-06", e.CreatedAt)
	}
}

func TestFileProviderIDFallsBackToFilename(t *testing.T) {
	p, dir := newProvider(t)
	// No id in frontmatter: the id is the filename stem.
	writeMemory(t, dir, "my-note.md", "---\ntitle: T\n---\nbody")
	if _, err := p.Get(context.Background(), "my-note"); err != nil {
		t.Errorf("Get by filename id: %v", err)
	}
}

func TestFileProviderGetNotFound(t *testing.T) {
	p, _ := newProvider(t)
	if _, err := p.Get(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestFileProviderMissingDirIsEmpty(t *testing.T) {
	p := NewFileProvider("ws1", filepath.Join(t.TempDir(), "does-not-exist"))
	hits, err := p.Search(context.Background(), "anything", "", 0)
	if err != nil || len(hits) != 0 {
		t.Errorf("search of missing dir = %+v, %v; want empty, nil", hits, err)
	}
	if _, err := p.Get(context.Background(), "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("get from missing dir err = %v, want ErrNotFound", err)
	}
}

func TestFileProviderReloadsOnChange(t *testing.T) {
	p, dir := newProvider(t)
	writeMemory(t, dir, "a.md", "---\nid: a\ntitle: alpha\n---\nbody")
	if hits, _ := p.Search(context.Background(), "alpha", "", 0); len(hits) != 1 {
		t.Fatalf("first search hits = %d, want 1", len(hits))
	}
	// A new file must be picked up on the next search (index refresh, not a grep).
	writeMemory(t, dir, "b.md", "---\nid: b\ntitle: alpha two\n---\nbody")
	if hits, _ := p.Search(context.Background(), "alpha", "", 0); len(hits) != 2 {
		t.Fatalf("after add, hits = %d, want 2", len(hits))
	}
}

func TestFileProviderNoFrontmatter(t *testing.T) {
	p, dir := newProvider(t)
	// A plain markdown file with no frontmatter is still indexed: id from filename,
	// whole content as body.
	writeMemory(t, dir, "plain.md", "just some notes about caching")
	e, err := p.Get(context.Background(), "plain")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if e.Text != "just some notes about caching" {
		t.Errorf("body = %q", e.Text)
	}
	if hits, _ := p.Search(context.Background(), "caching", "", 0); len(hits) != 1 {
		t.Errorf("search body-only = %d hits, want 1", len(hits))
	}
}

func TestFileProviderSearchLimit(t *testing.T) {
	p, dir := newProvider(t)
	for _, id := range []string{"a", "b", "c"} {
		writeMemory(t, dir, id+".md", "---\nid: "+id+"\ntitle: shared term\n---\nbody")
	}
	if hits, _ := p.Search(context.Background(), "shared", "", 2); len(hits) != 2 {
		t.Errorf("limited search = %d hits, want 2", len(hits))
	}
}

func TestFileProviderKindDefaultsToNote(t *testing.T) {
	p, dir := newProvider(t)
	writeMemory(t, dir, "n.md", "---\nid: n\ntitle: plain\n---\nbody")
	e, _ := p.Get(context.Background(), "n")
	if e.Kind != api.MemoryKindNote {
		t.Errorf("kind = %q, want note (default)", e.Kind)
	}
}

func TestFileProviderSearchFiltersByKind(t *testing.T) {
	p, dir := newProvider(t)
	writeMemory(t, dir, "note.md", "---\nid: note\ntitle: caching note\n---\nbody")
	writeMemory(t, dir, "rule.md", "---\nid: rule\nkind: steering\ntitle: caching rule\n---\nbody")

	// Unfiltered: both match.
	if hits, _ := p.Search(context.Background(), "caching", "", 0); len(hits) != 2 {
		t.Fatalf("unfiltered hits = %d, want 2", len(hits))
	}
	// kind=steering returns only the steering entry, carrying its kind.
	hits, _ := p.Search(context.Background(), "caching", api.MemoryKindSteering, 0)
	if len(hits) != 1 || hits[0].ID != "rule" || hits[0].Kind != api.MemoryKindSteering {
		t.Errorf("steering-filtered hits = %+v, want only rule", hits)
	}
}

func TestFileProviderWriteRoundTrips(t *testing.T) {
	p, _ := newProvider(t)
	in := api.MemoryWriteParams{
		WorkspaceID: "ws1",
		ID:          "runbook",
		Kind:        api.MemoryKindSteering,
		Title:       "Deploy runbook",
		Tags:        []string{"ops", "deploy"},
		Task:        &api.TaskRef{Provider: "beads", ID: "tend-7"},
		Text:        "  run make deploy  ",
	}
	e, err := p.Write(context.Background(), in)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if e.ID != "runbook" || e.Text != "run make deploy" {
		t.Errorf("returned entry = %+v (text should be trimmed)", e)
	}
	// Read it back through Get: the write must be indexed like any file.
	got, err := p.Get(context.Background(), "runbook")
	if err != nil {
		t.Fatalf("Get after write: %v", err)
	}
	if got.Title != "Deploy runbook" || got.Kind != api.MemoryKindSteering || len(got.Tags) != 2 {
		t.Errorf("read-back entry = %+v", got)
	}
	if got.Task == nil || got.Task.Provider != "beads" || got.Task.ID != "tend-7" || got.Task.WorkspaceID != "ws1" {
		t.Errorf("read-back task = %+v", got.Task)
	}
	if got.Text != "run make deploy" {
		t.Errorf("read-back text = %q", got.Text)
	}
}

func TestFileProviderWriteIsUpsertByID(t *testing.T) {
	p, dir := newProvider(t)
	if _, err := p.Write(context.Background(), api.MemoryWriteParams{WorkspaceID: "ws1", ID: "n", Title: "first", Text: "a"}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := p.Write(context.Background(), api.MemoryWriteParams{WorkspaceID: "ws1", ID: "n", Title: "second", Text: "b"}); err != nil {
		t.Fatalf("second write: %v", err)
	}
	// Same id overwrites rather than duplicating: one file, latest content.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("dir has %d files, want 1 (upsert)", len(entries))
	}
	got, _ := p.Get(context.Background(), "n")
	if got.Title != "second" || got.Text != "b" {
		t.Errorf("entry = %+v, want the second write", got)
	}
}

func TestFileProviderWriteDerivesIDFromTitle(t *testing.T) {
	p, _ := newProvider(t)
	e, err := p.Write(context.Background(), api.MemoryWriteParams{WorkspaceID: "ws1", Title: "Deploy the API!", Text: "x"})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if e.ID != "deploy-the-api" {
		t.Errorf("id = %q, want slug deploy-the-api", e.ID)
	}
}

func TestFileProviderWriteGeneratesIDWhenNoTitle(t *testing.T) {
	p, _ := newProvider(t)
	e, err := p.Write(context.Background(), api.MemoryWriteParams{WorkspaceID: "ws1", Text: "orphan note"})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !strings.HasPrefix(string(e.ID), "mem-") {
		t.Errorf("id = %q, want a generated mem- id", e.ID)
	}
	if _, err := p.Get(context.Background(), e.ID); err != nil {
		t.Errorf("Get generated id: %v", err)
	}
}

func TestFileProviderWriteCreatesMissingDir(t *testing.T) {
	// The memory dir does not exist yet: Write must create it.
	dir := filepath.Join(t.TempDir(), "nested", "memory")
	p := NewFileProvider("ws1", dir)
	if _, err := p.Write(context.Background(), api.MemoryWriteParams{WorkspaceID: "ws1", ID: "n", Text: "hi"}); err != nil {
		t.Fatalf("Write into missing dir: %v", err)
	}
	if _, err := p.Get(context.Background(), "n"); err != nil {
		t.Errorf("Get after creating dir: %v", err)
	}
}

func TestFileProviderWriteOmitsKindForNote(t *testing.T) {
	p, dir := newProvider(t)
	if _, err := p.Write(context.Background(), api.MemoryWriteParams{WorkspaceID: "ws1", ID: "n", Title: "t", Text: "b"}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "n.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "kind:") {
		t.Errorf("note file should not carry an explicit kind:\n%s", data)
	}
}

func TestFileProviderWriteRejectsUnsafeID(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "memory")
	p := NewFileProvider("ws1", dir)

	unsafe := []string{"../../README", "../escape", "a/b", `a\b`, "..", ".", "/etc/passwd", "sub/note"}
	for _, id := range unsafe {
		_, err := p.Write(context.Background(), api.MemoryWriteParams{WorkspaceID: "ws1", ID: api.MemoryID(id), Text: "x"})
		if !errors.Is(err, ErrInvalidID) {
			t.Errorf("Write(id=%q) err = %v, want ErrInvalidID", id, err)
		}
	}
	// No traversal wrote a file anywhere under the temp root — the memory dir was
	// never even created (rejection happens before any filesystem work).
	if _, err := os.Stat(filepath.Join(root, "README.md")); !os.IsNotExist(err) {
		t.Errorf("traversal wrote a file outside the memory dir: stat err = %v", err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Errorf("temp root has %d entries, want 0 (nothing created)", len(entries))
	}

	// A safe id still works and stays inside the memory dir.
	if _, err := p.Write(context.Background(), api.MemoryWriteParams{WorkspaceID: "ws1", ID: "ok.note-1", Text: "x"}); err != nil {
		t.Fatalf("safe id write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ok.note-1.md")); err != nil {
		t.Errorf("safe id file missing: %v", err)
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"**/*.go", "main.go", true},
		{"**/*.go", "internal/memory/file.go", true},
		{"**/*.go", "internal/memory/file.rs", false},
		{"*.go", "main.go", true},
		{"*.go", "internal/main.go", false}, // * does not cross a separator
		{"src/**/test_*.go", "src/a/b/test_x.go", true},
		{"src/**/test_*.go", "src/test_x.go", true}, // ** matches zero segments
		{"src/**/test_*.go", "lib/test_x.go", false},
		{"docs/**", "docs/a/b.md", true},
		{"docs/**", "docs", true}, // trailing ** matches zero segments
		{"docs/**", "src/a.md", false},
		{"api/*.go", "api/memory.go", true},
		{"?ain.go", "main.go", true},
	}
	for _, c := range cases {
		if got := globMatch(c.pattern, c.path); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

func TestFileProviderSteeringActivation(t *testing.T) {
	p, dir := newProvider(t)
	// A note is never steering, even if it somehow carried globs.
	writeMemory(t, dir, "note.md", "---\nid: note\ntitle: a note\n---\nbody")
	// always steering (no apply): always included.
	writeMemory(t, dir, "std.md", "---\nid: std\nkind: steering\ntitle: standards\n---\nalways on")
	// glob steering: included only when a glob matches the path.
	writeMemory(t, dir, "go.md", "---\nid: go\nkind: steering\napply: glob\nglobs: [\"**/*.go\"]\n---\ngo rules")
	// manual steering: never auto-included.
	writeMemory(t, dir, "man.md", "---\nid: man\nkind: steering\napply: manual\n---\nby hand only")

	ids := func(es []api.MemoryEntry) []string {
		out := make([]string, len(es))
		for i, e := range es {
			out[i] = string(e.ID)
		}
		return out
	}

	// Empty path: only always-steering (std), not the glob or manual, not the note.
	got, err := p.Steering(context.Background(), "")
	if err != nil {
		t.Fatalf("Steering: %v", err)
	}
	if g := ids(got); len(g) != 1 || g[0] != "std" {
		t.Errorf("empty-path steering = %v, want [std]", g)
	}

	// A .go path: always (std) + the matching glob (go), in id order.
	got, _ = p.Steering(context.Background(), "internal/memory/file.go")
	if g := ids(got); len(g) != 2 || g[0] != "go" || g[1] != "std" {
		t.Errorf("go-path steering = %v, want [go std] (id order)", g)
	}

	// A non-.go path: only always (std).
	got, _ = p.Steering(context.Background(), "README.md")
	if g := ids(got); len(g) != 1 || g[0] != "std" {
		t.Errorf("md-path steering = %v, want [std]", g)
	}
}

func TestFileProviderWriteSteeringRoundTrips(t *testing.T) {
	p, dir := newProvider(t)
	in := api.MemoryWriteParams{
		WorkspaceID: "ws1",
		ID:          "go-rules",
		Kind:        api.MemoryKindSteering,
		Apply:       api.MemoryApplyGlob,
		Globs:       []string{"**/*.go"},
		Title:       "Go rules",
		Text:        "use %w",
	}
	e, err := p.Write(context.Background(), in)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if e.Apply != api.MemoryApplyGlob || len(e.Globs) != 1 {
		t.Errorf("returned entry activation = %+v", e)
	}
	// The written file carries apply + globs and reads back through Steering.
	data, _ := os.ReadFile(filepath.Join(dir, "go-rules.md"))
	if !strings.Contains(string(data), "apply: glob") || !strings.Contains(string(data), "**/*.go") {
		t.Errorf("file missing activation frontmatter:\n%s", data)
	}
	got, _ := p.Steering(context.Background(), "cmd/tend/main.go")
	if len(got) != 1 || got[0].ID != "go-rules" {
		t.Errorf("steering after write = %+v, want the glob entry", got)
	}
}

func TestFileProviderSteeringDefaultsToAlways(t *testing.T) {
	p, _ := newProvider(t)
	// Steering written with no apply defaults to always and needs no path.
	if _, err := p.Write(context.Background(), api.MemoryWriteParams{
		WorkspaceID: "ws1", ID: "s", Kind: api.MemoryKindSteering, Text: "x",
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	e, _ := p.Get(context.Background(), "s")
	if e.Apply != api.MemoryApplyAlways {
		t.Errorf("apply = %q, want always (default for steering)", e.Apply)
	}
	if got, _ := p.Steering(context.Background(), ""); len(got) != 1 {
		t.Errorf("always steering not returned for empty path: %+v", got)
	}
}

func TestFileProviderUnknownApplyFailsClosed(t *testing.T) {
	p, dir := newProvider(t)
	// A typo'd apply value in a hand-edited file must NOT be treated as always: it
	// coerces to manual, so it is never auto-injected for any context.
	writeMemory(t, dir, "typo.md", "---\nid: typo\nkind: steering\napply: glbo\n---\nbad mode")
	e, err := p.Get(context.Background(), "typo")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if e.Apply != api.MemoryApplyManual {
		t.Errorf("apply = %q, want manual (fail closed)", e.Apply)
	}
	for _, path := range []string{"", "main.go", "anything"} {
		if got, _ := p.Steering(context.Background(), path); len(got) != 0 {
			t.Errorf("unknown-apply steering returned for path %q: %+v", path, got)
		}
	}
}

func TestFileProviderWriteRejectsUnknownApply(t *testing.T) {
	p, _ := newProvider(t)
	_, err := p.Write(context.Background(), api.MemoryWriteParams{
		WorkspaceID: "ws1", ID: "s", Kind: api.MemoryKindSteering, Apply: "bogus", Text: "x",
	})
	if !errors.Is(err, ErrInvalidApply) {
		t.Errorf("Write with bad apply err = %v, want ErrInvalidApply", err)
	}
	// A note ignores apply entirely, so a bad value there is not an error.
	if _, err := p.Write(context.Background(), api.MemoryWriteParams{
		WorkspaceID: "ws1", ID: "n", Apply: "bogus", Text: "x",
	}); err != nil {
		t.Errorf("note write with apply should not error: %v", err)
	}
}

func TestFileProviderNoteHasNoActivation(t *testing.T) {
	p, _ := newProvider(t)
	// A note write ignores apply/globs: they are steering-only.
	if _, err := p.Write(context.Background(), api.MemoryWriteParams{
		WorkspaceID: "ws1", ID: "n", Apply: api.MemoryApplyGlob, Globs: []string{"**"}, Text: "x",
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	e, _ := p.Get(context.Background(), "n")
	if e.Apply != "" || e.Globs != nil {
		t.Errorf("note carried activation: apply=%q globs=%v", e.Apply, e.Globs)
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Deploy the API!":  "deploy-the-api",
		"  spaced  out  ":  "spaced-out",
		"UPPER_snake-Case": "upper-snake-case",
		"!!!":              "",
		"":                 "",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplitFrontmatter(t *testing.T) {
	fm, body := splitFrontmatter([]byte("---\nid: x\n---\nbody here"))
	if strings.TrimSpace(string(fm)) != "id: x" || strings.TrimSpace(string(body)) != "body here" {
		t.Errorf("fm=%q body=%q", fm, body)
	}
	// No frontmatter: all body.
	fm, body = splitFrontmatter([]byte("no frontmatter"))
	if fm != nil || string(body) != "no frontmatter" {
		t.Errorf("fm=%q body=%q, want nil/all-body", fm, body)
	}
}

// entriesAreWorkspaceScoped: a provider tags every entry with its workspace.
func TestFileProviderEntriesCarryWorkspace(t *testing.T) {
	p := NewFileProvider(api.WorkspaceID("ws-9"), t.TempDir())
	writeMemory(t, p.dir, "a.md", "---\nid: a\n---\nbody")
	e, _ := p.Get(context.Background(), "a")
	if e.WorkspaceID != "ws-9" {
		t.Errorf("workspace = %q, want ws-9", e.WorkspaceID)
	}
}
