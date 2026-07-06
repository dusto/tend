package memimport

import (
	"path/filepath"
	"testing"

	"github.com/dusto/tend/api"
)

func TestCursorMapsRuleTypes(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".cursor", "rules")
	write(t, filepath.Join(dir, "always.mdc"), "---\nalwaysApply: true\ndescription: Core\n---\ncore rules")
	write(t, filepath.Join(dir, "ts.mdc"), "---\nglobs: '*.ts, *.tsx'\n---\n# TS\nts rules")
	write(t, filepath.Join(dir, "ask.mdc"), "---\ndescription: Ask me\n---\non request")

	items, err := Cursor{}.Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Item{}
	for _, it := range items {
		byID[it.ID] = it
	}

	if a := byID["cursor-always"]; a.Apply != api.MemoryApplyAlways || a.Title != "Core" {
		t.Errorf("always rule = %+v, want always + description title", a)
	}
	ts := byID["cursor-ts"]
	if ts.Apply != api.MemoryApplyGlob || len(ts.Globs) != 2 || ts.Globs[0] != "*.ts" || ts.Globs[1] != "*.tsx" {
		t.Errorf("ts rule = %+v, want glob with two comma-split globs", ts)
	}
	if ask := byID["cursor-ask"]; ask.Apply != api.MemoryApplyManual {
		t.Errorf("description-only rule apply = %q, want manual", ask.Apply)
	}
}

func TestCursorGlobsAsList(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ".cursor", "rules", "r.mdc"),
		"---\nglobs:\n  - '**/*.go'\n  - '**/*.md'\n---\nbody")
	items, _ := Cursor{}.Scan(root)
	if len(items) != 1 || len(items[0].Globs) != 2 {
		t.Fatalf("globs = %+v, want two", items)
	}
}

func TestCursorAbsentIsEmpty(t *testing.T) {
	if items, err := (Cursor{}).Scan(t.TempDir()); err != nil || items != nil {
		t.Errorf("absent .cursor = %+v, %v; want nil, nil", items, err)
	}
}

func TestCopilotImportsAsAlwaysSteering(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ".github", "copilot-instructions.md"), "# Copilot\nBe terse.")
	items, err := Copilot{}.Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d, want 1", len(items))
	}
	it := items[0]
	if it.ID != "copilot" || it.Apply != api.MemoryApplyAlways || it.Origin != ".github/copilot-instructions.md" {
		t.Errorf("item = %+v", it)
	}
}

func TestClaudeImportsCLAUDEmdAndMemoryNotes(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "CLAUDE.md"), "# Claude\nProject guidance.")
	write(t, filepath.Join(root, ".claude", "memory", "arch.md"), "# Architecture\nThe daemon owns state.")
	write(t, filepath.Join(root, ".claude", "memory", "gotcha.md"), "watch the mtime cache")

	items, err := Claude{}.Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Item{}
	for _, it := range items {
		byID[it.ID] = it
	}
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3 (CLAUDE.md + two notes)", len(items))
	}
	// CLAUDE.md is always-on steering.
	if c := byID["claude"]; c.Kind != api.MemoryKindSteering || c.Apply != api.MemoryApplyAlways || c.Origin != "CLAUDE.md" {
		t.Errorf("CLAUDE.md item = %+v, want always steering", c)
	}
	// Memory-dir files are episodic notes (no activation).
	arch := byID["claude-arch"]
	if arch.Kind != api.MemoryKindNote || arch.Apply != "" || arch.Origin != ".claude/memory/arch.md" {
		t.Errorf("arch note = %+v, want kind=note no activation", arch)
	}
	if byID["claude-gotcha"].Kind != api.MemoryKindNote {
		t.Errorf("gotcha kind = %q, want note", byID["claude-gotcha"].Kind)
	}
}

func TestClaudeOnlyCLAUDEmd(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "CLAUDE.md"), "# C\nx")
	items, _ := Claude{}.Scan(root)
	if len(items) != 1 || items[0].ID != "claude" {
		t.Fatalf("items = %+v, want just CLAUDE.md", items)
	}
}

func TestClaudeAbsentIsEmpty(t *testing.T) {
	if items, err := (Claude{}).Scan(t.TempDir()); err != nil || items != nil {
		t.Errorf("absent Claude files = %+v, %v; want nil, nil", items, err)
	}
}

func TestSelectIncludesNewAdapters(t *testing.T) {
	names := map[string]bool{}
	for _, n := range Sources() {
		names[n] = true
	}
	for _, want := range []string{"agents", "kiro", "cursor", "claude", "copilot"} {
		if !names[want] {
			t.Errorf("Sources() missing %q; got %v", want, Sources())
		}
	}
}
