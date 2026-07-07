package summarize

import (
	"context"
	"fmt"
	"strings"
)

// Fallback is the model-free summarizer: it returns content that already fits
// the budget unchanged, and otherwise truncates deterministically at a word or
// line boundary with an elision marker. It is always available, so summarization
// never hard-depends on a configured backend.
type Fallback struct {
	// TargetChars is the default budget; 0 uses DefaultTargetChars.
	TargetChars int
}

// Summarize truncates req.Text to the budget when it exceeds it. It never
// returns an error: a deterministic condense cannot fail.
func (f Fallback) Summarize(_ context.Context, req Request) (Result, error) {
	budget := req.TargetChars
	if budget <= 0 {
		budget = f.TargetChars
	}
	if budget <= 0 {
		budget = DefaultTargetChars
	}
	if len([]rune(req.Text)) <= budget {
		return Result{Text: req.Text, Summarized: false}, nil
	}
	return Result{Text: truncateToBudget(req.Text, budget), Summarized: false}, nil
}

// truncateToBudget cuts text to at most budget runes, preferring the last line
// or word boundary before the limit, and appends an elision marker noting how
// many characters were dropped. The marker itself is kept within budget.
func truncateToBudget(text string, budget int) string {
	runes := []rune(text)
	if len(runes) <= budget {
		return text
	}
	dropped := len(runes) - budget
	marker := fmt.Sprintf("\n[… %d characters truncated …]", dropped)
	// Reserve room for the marker; if the budget is smaller than the marker, the
	// marker alone (trimmed to budget) is the best we can do.
	keep := budget - len([]rune(marker))
	if keep <= 0 {
		return string([]rune(marker)[:budget])
	}
	head := string(runes[:keep])
	// Prefer a clean boundary: cut back to the last newline, else the last space,
	// as long as that does not discard more than a quarter of the kept text.
	if cut := boundary(head); cut > keep*3/4 {
		head = head[:cut]
	}
	return strings.TrimRight(head, " \t\n") + marker
}

// boundary returns the byte index just past the last line or word boundary in s,
// or len(s) when none is found.
func boundary(s string) int {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return i
	}
	if i := strings.LastIndexByte(s, ' '); i >= 0 {
		return i
	}
	return len(s)
}
