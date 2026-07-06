package memimport

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dusto/tend/api"
)

// Cursor imports Cursor rules (.cursor/rules/*.mdc) as steering memories, mapping
// Cursor's rule types to the TEND activation model:
//
//	alwaysApply: true      -> apply=always
//	globs: <patterns>      -> apply=glob   (auto-attached rules)
//	otherwise              -> apply=manual (agent-requested / description-only)
type Cursor struct{}

// Name identifies the adapter and is recorded as provenance source.
func (Cursor) Name() string { return "cursor" }

// cursorFrontmatter is the subset of a Cursor rule's frontmatter the adapter reads.
type cursorFrontmatter struct {
	Description string      `yaml:"description"`
	Globs       flexStrings `yaml:"globs"`
	AlwaysApply bool        `yaml:"alwaysApply"`
}

// Scan returns the steering items under <root>/.cursor/rules, or nil when the
// directory is absent.
func (c Cursor) Scan(root string) ([]Item, error) {
	dir := filepath.Join(root, ".cursor", "rules")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var items []Item
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".mdc") {
			continue
		}
		md, err := readMarkdown(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var fm cursorFrontmatter
		if err := md.decodeFrontmatter(&fm); err != nil {
			return nil, err
		}
		stem := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		apply, globs := cursorActivation(fm)
		items = append(items, Item{
			ID:     "cursor-" + slug(stem),
			Kind:   api.MemoryKindSteering,
			Apply:  apply,
			Globs:  globs,
			Title:  firstNonEmpty(strings.TrimSpace(fm.Description), titleOr(md.body, stem)),
			Text:   md.body,
			Origin: filepath.ToSlash(filepath.Join(".cursor", "rules", e.Name())),
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

// cursorActivation maps a Cursor rule's frontmatter to the TEND activation model.
// alwaysApply wins; otherwise globs make it a glob rule; otherwise it is manual
// (an agent-requested or description-only rule, not auto-injected).
func cursorActivation(fm cursorFrontmatter) (apply string, globs []string) {
	if fm.AlwaysApply {
		return api.MemoryApplyAlways, nil
	}
	if g := splitGlobs(fm.Globs); len(g) > 0 {
		return api.MemoryApplyGlob, g
	}
	return api.MemoryApplyManual, nil
}

// splitGlobs normalizes Cursor globs, which may arrive as a YAML list or as a
// single comma-separated scalar ("*.ts, *.tsx"): each entry is comma-split and
// trimmed, dropping empties.
func splitGlobs(raw []string) []string {
	var out []string
	for _, r := range raw {
		for part := range strings.SplitSeq(r, ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// firstNonEmpty returns a if non-empty, else b.
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
