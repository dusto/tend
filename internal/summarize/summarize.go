// Package summarize is TEND's pluggable summarizer: a small primitive that
// condenses text to a budget, used to reduce the context TEND injects (memory /
// steering), to collapse a session transcript range, and to build a resume seed.
// The summarization work is delegated to a Completer backend — a local model
// command or a dedicated ACP session — while this package owns the instruction,
// the budget, and a deterministic fallback so summarization always degrades
// cleanly. It deliberately does not import the acp package (acp embeds this
// package's Config), so an ACP-backed Completer is injected by the daemon rather
// than built here.
package summarize

import (
	"context"
	"fmt"
)

// Purpose hints how content should be condensed, so a backend can tailor its
// instruction. An empty Purpose is a generic condense.
const (
	// PurposeMemory condenses stored memory/steering for injection.
	PurposeMemory = "memory"
	// PurposeTranscript condenses a session transcript range.
	PurposeTranscript = "transcript"
	// PurposeResumeSeed condenses prior context into a "pick up where I left off"
	// seed for a new session.
	PurposeResumeSeed = "resume_seed"
)

// Request is one summarization request.
type Request struct {
	// Purpose is one of the Purpose* constants (or empty for a generic condense);
	// it selects the instruction the backend is asked to follow.
	Purpose string
	// Text is the content to condense.
	Text string
	// TargetChars is a soft budget for the output; 0 uses the Summarizer's
	// configured default.
	TargetChars int
}

// Result is a condensed output.
type Result struct {
	// Text is the condensed content.
	Text string
	// Summarized reports whether the content was reduced — condensed by a backend
	// or truncated by the fallback — rather than returned in full. It is false
	// only when the input already fit the budget and is returned verbatim, so a
	// caller can distinguish full context from a reduced digest.
	Summarized bool
}

// Summarizer condenses content to a budget.
type Summarizer interface {
	Summarize(ctx context.Context, req Request) (Result, error)
}

// Completer runs one text-completion prompt on a model and returns the model's
// text response. The local-model and ACP-session backends are Completers; this
// package builds the summarization prompt and enforces the budget, staying free
// of transport/provider specifics.
type Completer interface {
	Complete(ctx context.Context, prompt string) (string, error)
}

// New returns the Summarizer selected by cfg. Backend "none" (the default)
// returns the deterministic Fallback. Backend "local" builds a command Completer
// from cfg.Local. Backend "acp" uses acpCompleter, which the daemon injects with
// a dedicated-session adapter; passing nil for an "acp" backend is a wiring
// error. cfg is assumed already validated (see Config.Validate).
func New(cfg Config, acpCompleter Completer) (Summarizer, error) {
	fallback := Fallback{TargetChars: cfg.effectiveTargetChars()}
	switch cfg.Backend {
	case BackendNone, "":
		return fallback, nil
	case BackendLocal:
		return &completerSummarizer{
			completer: NewLocalCompleter(cfg.Local),
			fallback:  fallback,
			target:    cfg.effectiveTargetChars(),
		}, nil
	case BackendACP:
		if acpCompleter == nil {
			return nil, fmt.Errorf("summarize: acp backend selected but no acp completer is wired")
		}
		return &completerSummarizer{
			completer: acpCompleter,
			fallback:  fallback,
			target:    cfg.effectiveTargetChars(),
		}, nil
	default:
		return nil, fmt.Errorf("summarize: unknown backend %q", cfg.Backend)
	}
}

// completerSummarizer summarizes by prompting a Completer, and falls back to
// deterministic truncation when the content already fits or the backend fails.
type completerSummarizer struct {
	completer Completer
	fallback  Fallback
	target    int
}

// Summarize returns the input unchanged when it already fits the budget;
// otherwise it asks the backend to condense it, and on a backend error or an
// over-budget backend response it degrades to the deterministic fallback so a
// caller always gets a within-budget result.
func (s *completerSummarizer) Summarize(ctx context.Context, req Request) (Result, error) {
	budget := req.TargetChars
	if budget <= 0 {
		budget = s.target
	}
	if len([]rune(req.Text)) <= budget {
		return Result{Text: req.Text, Summarized: false}, nil
	}

	out, err := s.completer.Complete(ctx, buildPrompt(req.Purpose, budget, req.Text))
	if err != nil {
		// A backend failure must not deny the caller a usable result: degrade to
		// the deterministic fallback, which never errors.
		return s.fallback.Summarize(ctx, req)
	}
	// Guard against a backend that ignores the budget: never return over budget.
	if len([]rune(out)) > budget {
		out = truncateToBudget(out, budget)
	}
	return Result{Text: out, Summarized: true}, nil
}

// buildPrompt composes the instruction sent to a Completer: a purpose-specific
// directive, the character budget, and the content.
func buildPrompt(purpose string, budget int, text string) string {
	return fmt.Sprintf("%s\nKeep the result under %d characters. Output only the summary, no preamble.\n\n---\n%s",
		instructionFor(purpose), budget, text)
}

// instructionFor returns the summarization directive for a purpose.
func instructionFor(purpose string) string {
	switch purpose {
	case PurposeMemory:
		return "Condense the following project memory into the essential standing facts, rules, and decisions an agent must keep in mind. Drop redundancy and examples; preserve every distinct constraint."
	case PurposeTranscript:
		return "Summarize the following agent session transcript: what was attempted, what was decided, what changed, and what is left to do. Preserve concrete file names, identifiers, and outcomes."
	case PurposeResumeSeed:
		return "Write a concise handoff so another agent can pick up this work: the goal, the current state, key decisions, and the immediate next steps. Preserve concrete file names and identifiers."
	default:
		return "Condense the following content, preserving its essential facts and concrete details."
	}
}
