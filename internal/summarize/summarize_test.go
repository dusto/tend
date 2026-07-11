package summarize

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeCompleter is a Completer that returns a canned response (or error) and
// records the prompt it was given.
type fakeCompleter struct {
	reply  string
	err    error
	calls  int
	prompt string
}

func (f *fakeCompleter) Complete(_ context.Context, prompt string) (string, error) {
	f.calls++
	f.prompt = prompt
	return f.reply, f.err
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
		wantBk  Backend
	}{
		{"empty defaults to none", Config{}, false, BackendNone},
		{"explicit none", Config{Backend: BackendNone}, false, BackendNone},
		{"unknown backend", Config{Backend: "bogus"}, true, ""},
		{"local needs command", Config{Backend: BackendLocal}, true, ""},
		{"local ok", Config{Backend: BackendLocal, Local: LocalConfig{Command: "llm"}}, false, BackendLocal},
		{"acp needs provider", Config{Backend: BackendACP}, true, ""},
		{"acp ok", Config{Backend: BackendACP, ACP: ACPConfig{Provider: "claude"}}, false, BackendACP},
		{"negative budget", Config{TargetChars: -1}, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			err := cfg.Validate()
			if tt.wantErr != (err != nil) {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && cfg.Backend != tt.wantBk {
				t.Errorf("Backend = %q, want %q", cfg.Backend, tt.wantBk)
			}
		})
	}
}

func TestFallbackWithinBudgetIsUnchanged(t *testing.T) {
	f := Fallback{TargetChars: 100}
	res, err := f.Summarize(context.Background(), Request{Text: "short"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "short" || res.Summarized {
		t.Errorf("got %+v, want unchanged short", res)
	}
}

func TestFallbackTruncatesOverBudget(t *testing.T) {
	f := Fallback{TargetChars: 40}
	long := strings.Repeat("word ", 50) // 250 chars
	res, err := f.Summarize(context.Background(), Request{Text: long})
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(res.Text)); n > 40 {
		t.Errorf("result is %d runes, over budget 40: %q", n, res.Text)
	}
	if !strings.Contains(res.Text, "truncated") {
		t.Errorf("expected an elision marker, got %q", res.Text)
	}
	if !res.Summarized {
		t.Error("a truncated (reduced) result must report Summarized=true")
	}
}

func TestFallbackRequestBudgetOverridesDefault(t *testing.T) {
	f := Fallback{TargetChars: 1000}
	res, _ := f.Summarize(context.Background(), Request{Text: strings.Repeat("x", 200), TargetChars: 30})
	if n := len([]rune(res.Text)); n > 30 {
		t.Errorf("per-request budget ignored: %d runes", n)
	}
}

func TestCompleterSummarizerPassthroughWhenWithinBudget(t *testing.T) {
	fc := &fakeCompleter{reply: "should not be used"}
	s := &completerSummarizer{completer: fc, fallback: Fallback{}, target: 100}
	res, err := s.Summarize(context.Background(), Request{Text: "fits"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summarized || res.Text != "fits" {
		t.Errorf("got %+v, want unchanged", res)
	}
	if fc.calls != 0 {
		t.Errorf("completer called %d times for within-budget input", fc.calls)
	}
}

func TestCompleterSummarizerUsesBackendOverBudget(t *testing.T) {
	fc := &fakeCompleter{reply: "condensed"}
	s := &completerSummarizer{completer: fc, fallback: Fallback{}, target: 10}
	res, err := s.Summarize(context.Background(), Request{Purpose: PurposeMemory, Text: strings.Repeat("y", 100)})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Summarized || res.Text != "condensed" {
		t.Errorf("got %+v, want backend output", res)
	}
	if fc.calls != 1 {
		t.Fatalf("completer calls = %d, want 1", fc.calls)
	}
	if !strings.Contains(fc.prompt, "project memory") {
		t.Errorf("prompt missing purpose instruction: %q", fc.prompt)
	}
}

func TestCompleterSummarizerFallsBackOnError(t *testing.T) {
	fc := &fakeCompleter{err: errors.New("model down")}
	s := &completerSummarizer{completer: fc, fallback: Fallback{TargetChars: 20}, target: 20}
	res, err := s.Summarize(context.Background(), Request{Text: strings.Repeat("z", 200)})
	if err != nil {
		t.Fatalf("a backend error must degrade, not fail: %v", err)
	}
	if !res.Summarized {
		t.Error("a degraded-to-truncation result reduced the content, so Summarized=true")
	}
	if n := len([]rune(res.Text)); n > 20 {
		t.Errorf("degraded result over budget: %d runes", n)
	}
}

func TestCompleterSummarizerClampsOverBudgetBackend(t *testing.T) {
	// A backend that ignores the budget must not leak an over-budget result.
	fc := &fakeCompleter{reply: strings.Repeat("q", 500)}
	s := &completerSummarizer{completer: fc, fallback: Fallback{}, target: 30}
	res, _ := s.Summarize(context.Background(), Request{Text: strings.Repeat("y", 100)})
	if n := len([]rune(res.Text)); n > 30 {
		t.Errorf("over-budget backend output not clamped: %d runes", n)
	}
}

func TestNewSelectsBackend(t *testing.T) {
	if s, err := New(Config{Backend: BackendNone}, nil); err != nil {
		t.Fatalf("none: %v", err)
	} else if _, ok := s.(Fallback); !ok {
		t.Errorf("none backend = %T, want Fallback", s)
	}
	if _, err := New(Config{Backend: BackendACP, ACP: ACPConfig{Provider: "claude"}}, nil); err == nil {
		t.Error("acp backend with nil completer should be a wiring error")
	}
	if _, err := New(Config{Backend: BackendACP}, &fakeCompleter{}); err != nil {
		t.Errorf("acp backend with a completer should construct: %v", err)
	}
	if _, err := New(Config{Backend: "bogus"}, nil); err == nil {
		t.Error("unknown backend should error")
	}
}

func TestLocalCompleterRunsCommand(t *testing.T) {
	c := &LocalCompleter{
		command: "fake",
		run: func(_ context.Context, cmd string, _ []string, stdin string) (string, error) {
			if cmd != "fake" {
				t.Errorf("command = %q", cmd)
			}
			return "  summary from " + stdin + "  ", nil
		},
	}
	out, err := c.Complete(context.Background(), "the prompt")
	if err != nil {
		t.Fatal(err)
	}
	if out != "summary from the prompt" {
		t.Errorf("output = %q (should be trimmed)", out)
	}
}

func TestLocalCompleterEmptyCommand(t *testing.T) {
	c := &LocalCompleter{run: runCommand}
	if _, err := c.Complete(context.Background(), "x"); err == nil {
		t.Error("empty command should error")
	}
}

func TestRunCommandCapturesStdoutAndStderr(t *testing.T) {
	// A real command exercises the exec path: cat echoes stdin to stdout.
	out, err := runCommand(context.Background(), "cat", nil, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "hello" {
		t.Errorf("cat stdout = %q", out)
	}
	// A failing command surfaces its stderr.
	if _, err := runCommand(context.Background(), "sh", []string{"-c", "echo boom >&2; exit 1"}, ""); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("expected stderr in error, got %v", err)
	}
}
