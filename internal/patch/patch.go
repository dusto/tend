// Package patch implements the canonical v1 file-mutation engine: applying an
// ordered set of non-overlapping {line, byte_col} text edits to a base snapshot
// as a single transformation, and the content-hash base check used to detect a
// conflicting change between proposal and apply.
//
// Positions are {line, byte_col}: a 0-based line index and a byte offset within
// that line's UTF-8 bytes (not a UTF-16 code unit, not a rune index). Lines are
// split on '\n'; a '\r' immediately before a '\n' is preserved as part of that
// line and never normalized, and a byte_col may not point between '\r' and '\n'.
// An edit at end of line targets the position before the terminator; an edit at
// start of line targets byte_col 0 of the next line.
package patch

import (
	"errors"
	"fmt"
	"sort"

	"github.com/dusto/tend/api"
)

// Errors returned by Apply.
var (
	// ErrInvalidPosition reports a position outside the base, or one that points
	// between a '\r' and its following '\n'.
	ErrInvalidPosition = errors.New("patch: invalid position")
	// ErrInvalidRange reports an edit whose range start is after its end.
	ErrInvalidRange = errors.New("patch: range start after end")
	// ErrOverlap reports two edits whose ranges overlap.
	ErrOverlap = errors.New("patch: overlapping edits")
)

// Apply returns base transformed by edits. All ranges are interpreted against
// base (the snapshot), not against already-mutated content, so an earlier edit
// never shifts a later edit's offsets. Edits may be given in any order; they must
// be in range and non-overlapping (touching ranges are allowed).
func Apply(base []byte, edits []api.TextEdit) ([]byte, error) {
	li := indexLines(base)

	type resolved struct {
		start, end int
		text       string
		order      int
	}
	rs := make([]resolved, len(edits))
	for i, e := range edits {
		start, err := li.offset(e.Range.Start)
		if err != nil {
			return nil, err
		}
		end, err := li.offset(e.Range.End)
		if err != nil {
			return nil, err
		}
		if start > end {
			return nil, fmt.Errorf("%w: [%d,%d)", ErrInvalidRange, start, end)
		}
		rs[i] = resolved{start: start, end: end, text: e.NewText, order: i}
	}

	// Apply in position order; ties keep input order so adjacent inserts at the
	// same offset compose predictably.
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].start != rs[j].start {
			return rs[i].start < rs[j].start
		}
		return rs[i].order < rs[j].order
	})

	out := make([]byte, 0, len(base))
	cursor := 0
	for _, r := range rs {
		if r.start < cursor {
			return nil, fmt.Errorf("%w: edit at %d overlaps a prior edit ending at %d", ErrOverlap, r.start, cursor)
		}
		out = append(out, base[cursor:r.start]...)
		out = append(out, r.text...)
		cursor = r.end
	}
	out = append(out, base[cursor:]...)
	return out, nil
}

// lineIndex maps {line, byte_col} positions to byte offsets in a base snapshot.
type lineIndex struct {
	start  []int // byte offset where each line begins
	maxCol []int // largest valid byte_col on each line
}

// indexLines builds the line index for base. A line's content is the bytes
// between '\n' terminators; a trailing '\r' before a terminating '\n' stays in
// the line but is not an addressable end position (byte_col there would fall
// between '\r' and '\n').
func indexLines(base []byte) lineIndex {
	li := lineIndex{start: []int{0}}
	begin := 0
	for i := range len(base) {
		if base[i] != '\n' {
			continue
		}
		content := i - begin
		if content > 0 && base[i-1] == '\r' {
			content-- // the position before '\n' (between '\r' and '\n') is invalid
		}
		li.maxCol = append(li.maxCol, content)
		begin = i + 1
		li.start = append(li.start, begin)
	}
	li.maxCol = append(li.maxCol, len(base)-begin) // last line has no terminator
	return li
}

// offset converts a position to a byte offset, rejecting out-of-range lines and
// columns (including a column between '\r' and '\n').
func (li lineIndex) offset(p api.Position) (int, error) {
	if p.Line < 0 || p.Line >= len(li.start) {
		return 0, fmt.Errorf("%w: line %d out of range", ErrInvalidPosition, p.Line)
	}
	if p.ByteCol < 0 || p.ByteCol > li.maxCol[p.Line] {
		return 0, fmt.Errorf("%w: line %d byte_col %d (max %d)", ErrInvalidPosition, p.Line, p.ByteCol, li.maxCol[p.Line])
	}
	return li.start[p.Line] + p.ByteCol, nil
}
