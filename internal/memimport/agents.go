package memimport

import (
	"os"
	"path/filepath"

	"github.com/dusto/tend/api"
)

// Agents imports a repo's AGENTS.md as a single always-on steering memory. It is
// the simplest adapter: one file, no inclusion metadata.
type Agents struct{}

// Name identifies the adapter and is recorded as provenance source.
func (Agents) Name() string { return "agents" }

// Scan returns the AGENTS.md steering item, or nil when the file is absent.
func (Agents) Scan(root string) ([]Item, error) {
	return singleSteeringFile(root, "AGENTS.md", "agents")
}

// singleSteeringFile reads one always-on steering file at <root>/rel into a
// single item with the given id, or nil when the file is absent. It is the shared
// shape for the flat instruction-file adapters (AGENTS.md, Copilot, CLAUDE.md).
func singleSteeringFile(root, rel, id string) ([]Item, error) {
	path := filepath.Join(root, filepath.FromSlash(rel))
	md, err := readMarkdown(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if md.body == "" {
		return nil, nil
	}
	return []Item{{
		ID:     id,
		Kind:   api.MemoryKindSteering,
		Apply:  api.MemoryApplyAlways,
		Title:  titleOr(md.body, rel),
		Text:   md.body,
		Origin: rel,
	}}, nil
}
