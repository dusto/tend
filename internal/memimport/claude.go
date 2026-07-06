package memimport

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dusto/tend/api"
)

// Claude imports Claude Code memory: the project's CLAUDE.md as always-on
// steering, plus any episodic notes under a .claude/memory directory as notes
// (kind=note). Instruction/rule files are steering; the memory dir is episodic.
type Claude struct{}

// Name identifies the adapter and is recorded as provenance source.
func (Claude) Name() string { return "claude" }

// Scan returns CLAUDE.md as steering and .claude/memory/*.md as notes, or nil for
// whichever is absent.
func (c Claude) Scan(root string) ([]Item, error) {
	items, err := singleSteeringFile(root, "CLAUDE.md", "claude")
	if err != nil {
		return nil, err
	}
	notes, err := claudeMemoryNotes(root)
	if err != nil {
		return nil, err
	}
	return append(items, notes...), nil
}

// claudeMemoryNotes reads markdown files under <root>/.claude/memory as episodic
// note memories, or nil when the directory is absent.
func claudeMemoryNotes(root string) ([]Item, error) {
	dir := filepath.Join(root, ".claude", "memory")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var items []Item
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			continue
		}
		md, err := readMarkdown(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		if md.body == "" {
			continue
		}
		stem := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		items = append(items, Item{
			ID:     "claude-" + slug(stem),
			Kind:   api.MemoryKindNote,
			Title:  titleOr(md.body, stem),
			Text:   md.body,
			Origin: filepath.ToSlash(filepath.Join(".claude", "memory", e.Name())),
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}
