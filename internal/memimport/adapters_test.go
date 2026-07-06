package memimport

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dusto/tend/api"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestKiroMapsInclusionModes(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".kiro", "steering")
	write(t, filepath.Join(dir, "standards.md"), "# Standards\nalways on")
	write(t, filepath.Join(dir, "ts.md"), "---\ninclusion: fileMatch\nfileMatchPattern: '**/*.ts'\n---\n# TS rules\nbody")
	write(t, filepath.Join(dir, "onboard.md"), "---\ninclusion: manual\n---\n# Onboarding\nbody")

	items, err := Kiro{}.Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3", len(items))
	}
	byID := map[string]Item{}
	for _, it := range items {
		byID[it.ID] = it
	}

	std := byID["kiro-standards"]
	if std.Kind != api.MemoryKindSteering || std.Apply != api.MemoryApplyAlways {
		t.Errorf("standards = %+v, want always steering", std)
	}
	if std.Title != "Standards" || std.Origin != ".kiro/steering/standards.md" {
		t.Errorf("standards title/origin = %q/%q", std.Title, std.Origin)
	}

	ts := byID["kiro-ts"]
	if ts.Apply != api.MemoryApplyGlob || len(ts.Globs) != 1 || ts.Globs[0] != "**/*.ts" {
		t.Errorf("ts = %+v, want glob **/*.ts", ts)
	}

	if byID["kiro-onboard"].Apply != api.MemoryApplyManual {
		t.Errorf("onboard apply = %q, want manual", byID["kiro-onboard"].Apply)
	}
}

func TestKiroFileMatchPatternList(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ".kiro", "steering", "multi.md"),
		"---\ninclusion: fileMatch\nfileMatchPattern:\n  - '**/*.ts'\n  - '**/*.tsx'\n---\nbody")
	items, _ := Kiro{}.Scan(root)
	if len(items) != 1 || len(items[0].Globs) != 2 {
		t.Fatalf("globs = %+v, want two", items)
	}
}

func TestKiroAbsentIsEmpty(t *testing.T) {
	items, err := Kiro{}.Scan(t.TempDir())
	if err != nil || items != nil {
		t.Errorf("absent .kiro = %+v, %v; want nil, nil", items, err)
	}
}

func TestAgentsImportsAsAlwaysSteering(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "AGENTS.md"), "# Project\nGuardrails here.")
	items, err := Agents{}.Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d, want 1", len(items))
	}
	it := items[0]
	if it.ID != "agents" || it.Kind != api.MemoryKindSteering || it.Apply != api.MemoryApplyAlways {
		t.Errorf("item = %+v, want always steering id=agents", it)
	}
	if it.Title != "Project" || it.Origin != "AGENTS.md" {
		t.Errorf("title/origin = %q/%q", it.Title, it.Origin)
	}
}

func TestAgentsAbsentIsEmpty(t *testing.T) {
	items, err := Agents{}.Scan(t.TempDir())
	if err != nil || items != nil {
		t.Errorf("absent AGENTS.md = %+v, %v; want nil, nil", items, err)
	}
}

func TestSlugAndFirstHeading(t *testing.T) {
	if got := slug("Deploy the API!"); got != "deploy-the-api" {
		t.Errorf("slug = %q", got)
	}
	if got := firstHeading("no heading here\n## Second\n"); got != "Second" {
		t.Errorf("firstHeading = %q, want Second", got)
	}
	if got := firstHeading("plain text only"); got != "" {
		t.Errorf("firstHeading with none = %q, want empty", got)
	}
}
