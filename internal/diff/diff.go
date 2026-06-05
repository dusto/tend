// Package diff produces a line-based unified diff, used to put a human-reviewable
// representation of a proposed change into an approval payload so a client that
// is not the editor can decide without an editor preview.
package diff

import (
	"fmt"
	"strings"
)

// context is the number of unchanged lines shown around each change.
const context = 3

// Unified returns a unified diff of oldText against newText (line-based, with a
// few lines of context per hunk). It returns "" when the texts are equal. The
// output has @@ hunk headers and +/-/space line prefixes but no file headers;
// the file identity travels structurally in the approval payload.
func Unified(oldText, newText string) string {
	a, b := splitLines(oldText), splitLines(newText)
	rows := align(a, b)

	changed := make([]int, 0)
	for i, r := range rows {
		if r.kind != eq {
			changed = append(changed, i)
		}
	}
	if len(changed) == 0 {
		return ""
	}

	var out strings.Builder
	for _, h := range hunks(rows, changed) {
		writeHunk(&out, rows, h)
	}
	return out.String()
}

type kind int

const (
	eq kind = iota
	del
	ins
)

// row is one aligned line with its 1-based numbers in the old and new files
// (0 where the line does not exist on that side).
type row struct {
	kind       kind
	text       string
	oldN, newN int
}

// align computes a line-level edit script of a into b via an LCS, then numbers
// the lines.
func align(a, b []string) []row {
	n, m := len(a), len(b)
	// lcs[i][j] = length of the longest common subsequence of a[i:] and b[j:].
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}

	var rows []row
	oldN, newN := 1, 1
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			rows = append(rows, row{kind: eq, text: a[i], oldN: oldN, newN: newN})
			i, j, oldN, newN = i+1, j+1, oldN+1, newN+1
		case lcs[i+1][j] >= lcs[i][j+1]:
			rows = append(rows, row{kind: del, text: a[i], oldN: oldN})
			i, oldN = i+1, oldN+1
		default:
			rows = append(rows, row{kind: ins, text: b[j], newN: newN})
			j, newN = j+1, newN+1
		}
	}
	for ; i < n; i, oldN = i+1, oldN+1 {
		rows = append(rows, row{kind: del, text: a[i], oldN: oldN})
	}
	for ; j < m; j, newN = j+1, newN+1 {
		rows = append(rows, row{kind: ins, text: b[j], newN: newN})
	}
	return rows
}

// hunk is a half-open row range [start, end) to emit as one @@ block.
type hunk struct{ start, end int }

// hunks groups changed rows into hunks, padding each with context lines and
// merging hunks whose context windows touch.
func hunks(rows []row, changed []int) []hunk {
	var hs []hunk
	for _, c := range changed {
		start := max(c-context, 0)
		end := min(c+context+1, len(rows))
		if n := len(hs); n > 0 && start <= hs[n-1].end {
			hs[n-1].end = max(hs[n-1].end, end)
			continue
		}
		hs = append(hs, hunk{start: start, end: end})
	}
	return hs
}

func writeHunk(out *strings.Builder, rows []row, h hunk) {
	oldStart, oldCount, newStart, newCount := 0, 0, 0, 0
	for _, r := range rows[h.start:h.end] {
		if r.kind != ins {
			if oldStart == 0 {
				oldStart = r.oldN
			}
			oldCount++
		}
		if r.kind != del {
			if newStart == 0 {
				newStart = r.newN
			}
			newCount++
		}
	}
	fmt.Fprintf(out, "@@ -%s +%s @@\n", span(oldStart, oldCount), span(newStart, newCount))
	for _, r := range rows[h.start:h.end] {
		switch r.kind {
		case eq:
			out.WriteByte(' ')
		case del:
			out.WriteByte('-')
		case ins:
			out.WriteByte('+')
		}
		out.WriteString(r.text)
		out.WriteByte('\n')
	}
}

// span renders a unified-diff range: "start,count", or just "start" for a single
// line, and "0,0" for an empty side.
func span(start, count int) string {
	if count == 0 {
		return "0,0"
	}
	if count == 1 {
		return fmt.Sprintf("%d", start)
	}
	return fmt.Sprintf("%d,%d", start, count)
}

// splitLines splits text into lines, dropping the trailing empty element after a
// final newline so "a\nb\n" is two lines, not three.
func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
