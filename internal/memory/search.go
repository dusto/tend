package memory

import (
	"strings"

	"github.com/dusto/tend/api"
)

// titleWeight scores a term found in a memory's title or tags higher than one
// found only in the body, so the most on-topic notes rank first.
const titleWeight = 3

// terms lowercases and splits a query into its distinct search terms.
func terms(query string) []string {
	seen := make(map[string]struct{})
	var out []string
	for t := range strings.FieldsSeq(strings.ToLower(query)) {
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// score rates how well an entry matches the terms: each term present in the
// title/tags scores titleWeight, each present in the body scores 1. A term
// missing everywhere contributes nothing; an entry with no matched term scores 0.
func score(e api.MemoryEntry, terms []string) int {
	head := strings.ToLower(e.Title + " " + strings.Join(e.Tags, " "))
	body := strings.ToLower(e.Text)
	total := 0
	for _, t := range terms {
		switch {
		case strings.Contains(head, t):
			total += titleWeight
		case strings.Contains(body, t):
			total++
		}
	}
	return total
}

// snippet returns a short body excerpt around the first matching term, or the
// head of the body when only the title/tags matched. Whitespace is collapsed and
// ellipses mark a trimmed edge.
func snippet(text string, terms []string) string {
	collapsed := strings.Join(strings.Fields(text), " ")
	if collapsed == "" {
		return ""
	}
	lower := strings.ToLower(collapsed)
	first := -1
	for _, t := range terms {
		if i := strings.Index(lower, t); i >= 0 && (first < 0 || i < first) {
			first = i
		}
	}
	if first < 0 {
		return clip(collapsed, 0, snippetBefore+snippetAfter)
	}
	start := max(0, first-snippetBefore)
	return clip(collapsed, start, first+snippetAfter)
}

// clip returns collapsed[start:end] bounded to the string, prefixing/suffixing an
// ellipsis when an edge was trimmed.
func clip(s string, start, end int) string {
	if end > len(s) {
		end = len(s)
	}
	if start < 0 {
		start = 0
	}
	out := s[start:end]
	if start > 0 {
		out = "…" + out
	}
	if end < len(s) {
		out += "…"
	}
	return out
}
