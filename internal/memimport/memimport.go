// Package memimport is a one-shot importer that maps external agent
// memory/instruction files (Kiro steering, AGENTS.md, ...) into TEND memory
// entries. Each imported entry carries provenance (source, origin path, content
// hash), so a re-import updates the entry in place without duplicating it or
// clobbering a human's later edits. The adapters are pure file parsers; the
// engine talks to memory through a small Store interface, so it is unit-testable
// without a daemon.
package memimport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/dusto/tend/api"
)

// registry lists every available adapter in a stable order. New adapters are
// added here.
func registry() []Adapter { return []Adapter{Agents{}, Kiro{}} }

// Sources returns the names of every available adapter, for help/validation.
func Sources() []string {
	all := registry()
	names := make([]string, len(all))
	for i, a := range all {
		names[i] = a.Name()
	}
	return names
}

// Select returns the adapters for the given source names. The name "all" (or an
// empty list) selects every adapter; an unknown name is an error.
func Select(names []string) ([]Adapter, error) {
	byName := make(map[string]Adapter)
	for _, a := range registry() {
		byName[a.Name()] = a
	}
	if len(names) == 0 {
		return registry(), nil
	}
	var out []Adapter
	for _, n := range names {
		if n == "all" {
			return registry(), nil
		}
		a, ok := byName[n]
		if !ok {
			return nil, fmt.Errorf("memimport: unknown source %q (known: %s)", n, strings.Join(Sources(), ", "))
		}
		out = append(out, a)
	}
	return out, nil
}

// Item is one memory entry an adapter extracted from a source file.
type Item struct {
	// ID is the stable target id (deterministic per source file, so re-imports
	// address the same entry).
	ID string
	// Kind is note or steering.
	Kind string
	// Apply and Globs are the steering activation (empty for notes).
	Apply string
	Globs []string
	Title string
	// Text is the entry body.
	Text string
	// Origin is the source file path relative to the repo root, recorded as
	// provenance.
	Origin string
}

// Adapter discovers and parses one tool's memory/instruction files under a repo
// root into importable items.
type Adapter interface {
	// Name identifies the adapter; it is recorded as each item's provenance source.
	Name() string
	// Scan returns the items found under root, or nil when the source is absent.
	Scan(root string) ([]Item, error)
}

// Store is the subset of the memory service the importer needs. The CLI backs it
// with memory.get / memory.write over the daemon.
type Store interface {
	// Get returns the entry for id and whether it exists.
	Get(ctx context.Context, id string) (api.MemoryEntry, bool, error)
	// Write upserts an entry.
	Write(ctx context.Context, params api.MemoryWriteParams) (api.MemoryEntry, error)
}

// Status is the outcome of importing one item.
type Status string

const (
	// StatusCreated means the entry did not exist and was written.
	StatusCreated Status = "created"
	// StatusUpdated means an import-owned, unedited entry changed and was rewritten.
	StatusUpdated Status = "updated"
	// StatusUnchanged means the source matches the stored entry; nothing written.
	StatusUnchanged Status = "unchanged"
	// StatusSkipped means the entry was left untouched (see Reason).
	StatusSkipped Status = "skipped"
)

// Outcome reports what happened to one item.
type Outcome struct {
	Source string
	Origin string
	ID     string
	Status Status
	// Reason explains a StatusSkipped outcome (e.g. human-edited, id conflict).
	Reason string
}

// Result is the full report of an import run.
type Result struct {
	Outcomes []Outcome
}

// Counts tallies outcomes by status.
func (r Result) Counts() map[Status]int {
	c := make(map[Status]int, 4)
	for _, o := range r.Outcomes {
		c[o.Status]++
	}
	return c
}

// Run imports every adapter's items (found under root) into the store. dryRun
// reports what would happen without writing. Adapters run in the given order and
// each adapter's items in id order, so the report is deterministic.
func Run(ctx context.Context, store Store, root string, adapters []Adapter, dryRun bool) (Result, error) {
	var res Result
	for _, a := range adapters {
		items, err := a.Scan(root)
		if err != nil {
			return res, fmt.Errorf("memimport: scan %s: %w", a.Name(), err)
		}
		sortItems(items)
		for _, it := range items {
			out, err := importItem(ctx, store, a.Name(), it, dryRun)
			if err != nil {
				return res, fmt.Errorf("memimport: %s %s: %w", a.Name(), it.Origin, err)
			}
			res.Outcomes = append(res.Outcomes, out)
		}
	}
	return res, nil
}

// hashOwned is the provenance hash recorded on import and used to detect a later
// human edit. It covers the full normalized owned state (title/activation/body),
// not just the body, so an edit to any field the importer owns is detected and
// preserved. The hash is stable across a write/read round-trip: it is computed
// identically from a source Item and from the entry the store later returns.
func hashOwned(s ownedState) string {
	s.Text = strings.TrimSpace(s.Text)
	// json.Marshal is deterministic here: fixed field order, slice order preserved.
	b, _ := json.Marshal(s)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ownedState is the subset of an entry the importer authors and therefore owns.
// A human's edit to any of these fields must not be clobbered by a re-import, so
// the provenance hash covers all of them.
type ownedState struct {
	Kind  string   `json:"kind"`
	Apply string   `json:"apply"`
	Globs []string `json:"globs"`
	Title string   `json:"title"`
	Text  string   `json:"text"`
}

// ownedFromItem is the owned state an item would be stored as, applying the same
// activation normalization the store does, so it matches the stored entry.
func ownedFromItem(it Item) ownedState {
	apply, globs := normalizeActivation(it.Kind, it.Apply, it.Globs)
	return ownedState{Kind: it.Kind, Apply: apply, Globs: globs, Title: it.Title, Text: it.Text}
}

// ownedFromEntry is a stored entry's owned state; its activation is already
// normalized by the store on read.
func ownedFromEntry(e api.MemoryEntry) ownedState {
	return ownedState{Kind: e.Kind, Apply: e.Apply, Globs: e.Globs, Title: e.Title, Text: e.Text}
}

// normalizeActivation mirrors the store's steering normalization: notes carry no
// activation, steering defaults to always, and globs are kept only in glob mode.
func normalizeActivation(kind, apply string, globs []string) (string, []string) {
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

// importItem applies one item to the store under the re-import safety rules and
// returns its outcome.
func importItem(ctx context.Context, store Store, source string, it Item, dryRun bool) (Outcome, error) {
	out := Outcome{Source: source, Origin: it.Origin, ID: it.ID}
	wantHash := hashOwned(ownedFromItem(it))

	existing, found, err := store.Get(ctx, it.ID)
	if err != nil {
		return out, err
	}
	if found {
		// The id is occupied. Only overwrite an entry this same source imported and
		// whose owned fields a human has not edited since.
		pv := existing.Provenance
		switch {
		case pv == nil || pv.Source != source || pv.Origin != it.Origin:
			out.Status, out.Reason = StatusSkipped, "id already used by a non-import or different-source entry"
			return out, nil
		case hashOwned(ownedFromEntry(existing)) != pv.Hash:
			out.Status, out.Reason = StatusSkipped, "entry was edited since import; not overwriting"
			return out, nil
		case wantHash == pv.Hash:
			// Pristine and the source still produces the same state: nothing to do.
			out.Status = StatusUnchanged
			return out, nil
		}
	}

	if !dryRun {
		if _, err := store.Write(ctx, writeParams(existing, source, it, wantHash)); err != nil {
			return out, err
		}
	}
	if found {
		out.Status = StatusUpdated
	} else {
		out.Status = StatusCreated
	}
	return out, nil
}

// writeParams builds the memory.write params for an item, preserving the
// workspace of an existing entry.
func writeParams(existing api.MemoryEntry, source string, it Item, hash string) api.MemoryWriteParams {
	return api.MemoryWriteParams{
		WorkspaceID: existing.WorkspaceID,
		ID:          api.MemoryID(it.ID),
		Kind:        it.Kind,
		Apply:       it.Apply,
		Globs:       it.Globs,
		Title:       it.Title,
		Text:        it.Text,
		Provenance:  &api.MemoryProvenance{Source: source, Origin: it.Origin, Hash: hash},
	}
}

// sortItems orders items by id for a deterministic report.
func sortItems(items []Item) {
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
}
