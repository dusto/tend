package memimport

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dusto/tend/api"
)

// Kiro imports Kiro steering files (.kiro/steering/*.md) as steering memories,
// mapping Kiro's inclusion modes to the TEND activation model:
//
//	inclusion: always (or unset) -> apply=always
//	inclusion: fileMatch         -> apply=glob   (globs from fileMatchPattern)
//	inclusion: manual            -> apply=manual
type Kiro struct{}

// Name identifies the adapter and is recorded as provenance source.
func (Kiro) Name() string { return "kiro" }

// kiroFrontmatter is the subset of a Kiro steering file's frontmatter the adapter
// reads.
type kiroFrontmatter struct {
	Inclusion        string      `yaml:"inclusion"`
	FileMatchPattern flexStrings `yaml:"fileMatchPattern"`
}

// Scan returns the steering items under <root>/.kiro/steering, or nil when the
// directory is absent.
func (k Kiro) Scan(root string) ([]Item, error) {
	dir := filepath.Join(root, ".kiro", "steering")
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
		var fm kiroFrontmatter
		if err := md.decodeFrontmatter(&fm); err != nil {
			return nil, err
		}
		stem := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		apply, globs := kiroActivation(fm)
		items = append(items, Item{
			ID:     "kiro-" + slug(stem),
			Kind:   api.MemoryKindSteering,
			Apply:  apply,
			Globs:  globs,
			Title:  titleOr(md.body, stem),
			Text:   md.body,
			Origin: filepath.ToSlash(filepath.Join(".kiro", "steering", e.Name())),
		})
	}
	// ReadDir is already sorted, but keep the mapping explicit and stable.
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

// kiroActivation maps a Kiro inclusion mode to the TEND activation model.
func kiroActivation(fm kiroFrontmatter) (apply string, globs []string) {
	switch strings.ToLower(strings.TrimSpace(fm.Inclusion)) {
	case "filematch":
		return api.MemoryApplyGlob, fm.FileMatchPattern
	case "manual":
		return api.MemoryApplyManual, nil
	default: // "always" or unset
		return api.MemoryApplyAlways, nil
	}
}
