package memimport

import (
	"context"
	"strings"
	"testing"

	"github.com/dusto/tend/api"
)

// fakeStore is an in-memory Store for engine tests.
type fakeStore struct {
	entries map[string]api.MemoryEntry
	writes  int
}

func newStore() *fakeStore { return &fakeStore{entries: map[string]api.MemoryEntry{}} }

func (s *fakeStore) Get(_ context.Context, id string) (api.MemoryEntry, bool, error) {
	e, ok := s.entries[id]
	return e, ok, nil
}

func (s *fakeStore) Write(_ context.Context, p api.MemoryWriteParams) (api.MemoryEntry, error) {
	s.writes++
	// The file provider persists the trimmed body; mirror that so a stored entry's
	// text matches what hashBody hashes.
	e := api.MemoryEntry{
		ID: p.ID, WorkspaceID: p.WorkspaceID, Kind: p.Kind, Apply: p.Apply, Globs: p.Globs,
		Title: p.Title, Text: strings.TrimSpace(p.Text), Provenance: p.Provenance,
	}
	s.entries[string(p.ID)] = e
	return e, nil
}

func item(id, text string) Item {
	return Item{ID: id, Kind: api.MemoryKindSteering, Apply: api.MemoryApplyAlways, Title: "T", Text: text, Origin: "AGENTS.md"}
}

func TestImportItemCreates(t *testing.T) {
	s := newStore()
	out, err := importItem(context.Background(), s, "agents", item("agents", "hello"), false)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != StatusCreated {
		t.Fatalf("status = %q, want created", out.Status)
	}
	e := s.entries["agents"]
	if e.Provenance == nil || e.Provenance.Source != "agents" || e.Provenance.Origin != "AGENTS.md" {
		t.Errorf("provenance = %+v", e.Provenance)
	}
	if e.Provenance.Hash != hashBody("hello") {
		t.Errorf("hash = %q, want %q", e.Provenance.Hash, hashBody("hello"))
	}
}

func TestImportItemUnchangedIsNoop(t *testing.T) {
	s := newStore()
	if _, err := importItem(context.Background(), s, "agents", item("agents", "hello"), false); err != nil {
		t.Fatal(err)
	}
	before := s.writes
	out, err := importItem(context.Background(), s, "agents", item("agents", "hello"), false)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != StatusUnchanged {
		t.Fatalf("status = %q, want unchanged", out.Status)
	}
	if s.writes != before {
		t.Errorf("unchanged re-import wrote (%d writes), want no write", s.writes-before)
	}
}

func TestImportItemUpdatesWhenSourceChanges(t *testing.T) {
	s := newStore()
	if _, err := importItem(context.Background(), s, "agents", item("agents", "v1"), false); err != nil {
		t.Fatal(err)
	}
	out, err := importItem(context.Background(), s, "agents", item("agents", "v2"), false)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != StatusUpdated {
		t.Fatalf("status = %q, want updated", out.Status)
	}
	if s.entries["agents"].Text != "v2" {
		t.Errorf("text = %q, want v2", s.entries["agents"].Text)
	}
	if s.entries["agents"].Provenance.Hash != hashBody("v2") {
		t.Error("hash not refreshed on update")
	}
}

func TestImportItemSkipsHumanEdited(t *testing.T) {
	s := newStore()
	if _, err := importItem(context.Background(), s, "agents", item("agents", "v1"), false); err != nil {
		t.Fatal(err)
	}
	// Simulate a human editing the stored body (provenance hash now stale).
	e := s.entries["agents"]
	e.Text = "human edited this"
	s.entries["agents"] = e
	before := s.writes

	out, err := importItem(context.Background(), s, "agents", item("agents", "v2"), false)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != StatusSkipped {
		t.Fatalf("status = %q, want skipped", out.Status)
	}
	if s.writes != before {
		t.Error("human-edited entry must not be overwritten")
	}
	if s.entries["agents"].Text != "human edited this" {
		t.Error("human edit was clobbered")
	}
}

func TestImportItemSkipsIDConflict(t *testing.T) {
	s := newStore()
	// A hand-authored entry (no provenance) occupies the id.
	s.entries["agents"] = api.MemoryEntry{ID: "agents", Text: "mine", Kind: api.MemoryKindNote}
	out, err := importItem(context.Background(), s, "agents", item("agents", "v1"), false)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != StatusSkipped {
		t.Fatalf("status = %q, want skipped (conflict)", out.Status)
	}
	if s.writes != 0 {
		t.Error("must not clobber a non-import entry")
	}
}

func TestImportItemForeignSourceConflict(t *testing.T) {
	s := newStore()
	// An entry imported by a different source holds the id.
	s.entries["agents"] = api.MemoryEntry{
		ID: "agents", Text: "x", Kind: api.MemoryKindSteering,
		Provenance: &api.MemoryProvenance{Source: "kiro", Origin: "AGENTS.md", Hash: hashBody("x")},
	}
	out, _ := importItem(context.Background(), s, "agents", item("agents", "v1"), false)
	if out.Status != StatusSkipped {
		t.Fatalf("status = %q, want skipped (foreign source)", out.Status)
	}
}

func TestImportItemDryRunWritesNothing(t *testing.T) {
	s := newStore()
	out, err := importItem(context.Background(), s, "agents", item("agents", "hello"), true)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != StatusCreated {
		t.Fatalf("dry-run status = %q, want created (what would happen)", out.Status)
	}
	if s.writes != 0 {
		t.Error("dry-run must not write")
	}
}

func TestRunReportsAndCounts(t *testing.T) {
	s := newStore()
	// Seed one entry so a second run reports unchanged.
	adapters := []Adapter{staticAdapter{name: "agents", items: []Item{item("agents", "hi")}}}
	res, err := Run(context.Background(), s, "/root", adapters, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Counts()[StatusCreated]; got != 1 {
		t.Fatalf("created = %d, want 1", got)
	}
	res, _ = Run(context.Background(), s, "/root", adapters, false)
	if got := res.Counts()[StatusUnchanged]; got != 1 {
		t.Errorf("second run unchanged = %d, want 1", got)
	}
}

// staticAdapter returns a fixed item set, for Run tests.
type staticAdapter struct {
	name  string
	items []Item
}

func (a staticAdapter) Name() string                { return a.name }
func (a staticAdapter) Scan(string) ([]Item, error) { return a.items, nil }

func TestSelect(t *testing.T) {
	if all, _ := Select(nil); len(all) != len(Sources()) {
		t.Errorf("empty select = %d adapters, want all", len(all))
	}
	if one, err := Select([]string{"kiro"}); err != nil || len(one) != 1 || one[0].Name() != "kiro" {
		t.Errorf("Select(kiro) = %+v, %v", one, err)
	}
	if _, err := Select([]string{"nope"}); err == nil {
		t.Error("unknown source should error")
	}
}
