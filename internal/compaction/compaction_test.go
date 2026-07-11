package compaction

import (
	"context"
	"sync"
	"testing"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/session"
)

// fakePrompter records forwarded prompts. An optional onPrompt hook lets a test
// re-enter the trigger (to exercise the re-entrancy guard).
type fakePrompter struct {
	mu       sync.Mutex
	prompts  []api.AgentPromptParams
	onPrompt func(api.AgentPromptParams)
}

func (f *fakePrompter) Prompt(_ context.Context, p api.AgentPromptParams) (api.AgentPromptResult, error) {
	f.mu.Lock()
	f.prompts = append(f.prompts, p)
	f.mu.Unlock()
	if f.onPrompt != nil {
		f.onPrompt(p)
	}
	return api.AgentPromptResult{StopReason: "end_turn", Status: api.StatusIdle}, nil
}

func (f *fakePrompter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.prompts)
}

// fakeTranscript records fallback compactions.
type fakeTranscript struct {
	calls []struct {
		stream   api.StreamID
		from, to uint64
		budget   int
	}
}

func (f *fakeTranscript) Compact(_ context.Context, stream api.StreamID, _ api.SessionID, from, to uint64, budget int) error {
	f.calls = append(f.calls, struct {
		stream   api.StreamID
		from, to uint64
		budget   int
	}{stream, from, to, budget})
	return nil
}

// fakeRanger returns a fixed compactable range (ok controls whether one exists).
type fakeRanger struct {
	from, to uint64
	ok       bool
}

func (f fakeRanger) CompactableRange(api.StreamID) (uint64, uint64, bool) {
	return f.from, f.to, f.ok
}

// newSession registers an idle session and returns the registry and its id.
func newSession(t *testing.T) (*session.Registry, api.SessionID) {
	t.Helper()
	reg := session.NewRegistry()
	id := api.SessionID("s1")
	reg.Create(id, "codex", "ws1", api.TaskRef{}, "/root")
	return reg, id
}

func TestMaybeCompactBelowThresholdDoesNothing(t *testing.T) {
	reg, id := newSession(t)
	sess, _ := reg.Get(id)
	sess.SetContextUsage(50, 100) // 0.5 fullness

	pr := &fakePrompter{}
	tr := &fakeTranscript{}
	s := NewService(reg, pr, tr, fakeRanger{ok: true, from: 1, to: 10}, 0.85, 0)
	s.MaybeCompact(context.Background(), id)

	if pr.count() != 0 || len(tr.calls) != 0 {
		t.Fatalf("below threshold should not act: prompts=%d compacts=%d", pr.count(), len(tr.calls))
	}
}

func TestMaybeCompactNoUsageReportedIsNoOp(t *testing.T) {
	reg, id := newSession(t)
	// No SetContextUsage: window is 0, so fullness is unknown.
	pr := &fakePrompter{}
	tr := &fakeTranscript{}
	s := NewService(reg, pr, tr, fakeRanger{ok: true}, 0.85, 0)
	s.MaybeCompact(context.Background(), id)

	if pr.count() != 0 || len(tr.calls) != 0 {
		t.Fatalf("no usage should not act: prompts=%d compacts=%d", pr.count(), len(tr.calls))
	}
}

func TestMaybeCompactForwardsProviderCommand(t *testing.T) {
	reg, id := newSession(t)
	sess, _ := reg.Get(id)
	sess.SetContextUsage(90, 100) // 0.9 fullness, over 0.85
	sess.SetProviderCommands([]api.SlashCommand{{Name: "compact"}})

	pr := &fakePrompter{}
	tr := &fakeTranscript{}
	s := NewService(reg, pr, tr, fakeRanger{ok: true, from: 1, to: 10}, 0.85, 0)
	s.MaybeCompact(context.Background(), id)

	if pr.count() != 1 {
		t.Fatalf("want one forwarded /compact, got %d", pr.count())
	}
	if got := pr.prompts[0].Text; got != "/compact" {
		t.Fatalf("forwarded text = %q, want /compact", got)
	}
	if len(tr.calls) != 0 {
		t.Fatalf("provider command present: fallback must not run, got %d compacts", len(tr.calls))
	}
}

func TestMaybeCompactFallsBackToTranscript(t *testing.T) {
	reg, id := newSession(t)
	sess, _ := reg.Get(id)
	sess.SetContextUsage(90, 100)
	// No provider /compact command advertised.

	pr := &fakePrompter{}
	tr := &fakeTranscript{}
	s := NewService(reg, pr, tr, fakeRanger{ok: true, from: 3, to: 42}, 0.85, 1500)
	s.MaybeCompact(context.Background(), id)

	if pr.count() != 0 {
		t.Fatalf("no provider command: must not forward a prompt, got %d", pr.count())
	}
	if len(tr.calls) != 1 {
		t.Fatalf("want one fallback compaction, got %d", len(tr.calls))
	}
	c := tr.calls[0]
	if c.from != 3 || c.to != 42 || c.budget != 1500 || c.stream != sess.Stream {
		t.Fatalf("fallback compacted %+v, want stream=%s [3,42] budget=1500", c, sess.Stream)
	}
}

func TestMaybeCompactFallbackNoRangeIsNoOp(t *testing.T) {
	reg, id := newSession(t)
	sess, _ := reg.Get(id)
	sess.SetContextUsage(99, 100)

	pr := &fakePrompter{}
	tr := &fakeTranscript{}
	s := NewService(reg, pr, tr, fakeRanger{ok: false}, 0.85, 0)
	s.MaybeCompact(context.Background(), id)

	if len(tr.calls) != 0 {
		t.Fatalf("nothing compactable: must not compact, got %d", len(tr.calls))
	}
}

func TestMaybeCompactReentrancyGuard(t *testing.T) {
	reg, id := newSession(t)
	sess, _ := reg.Get(id)
	sess.SetContextUsage(95, 100)
	sess.SetProviderCommands([]api.SlashCommand{{Name: "compact"}})

	pr := &fakePrompter{}
	tr := &fakeTranscript{}
	s := NewService(reg, pr, tr, fakeRanger{ok: true, from: 1, to: 10}, 0.85, 0)
	// The forwarded /compact turn re-enters the trigger, as the real agent service
	// does after any turn. The guard must make that inner call a no-op.
	pr.onPrompt = func(api.AgentPromptParams) {
		s.MaybeCompact(context.Background(), id)
	}
	s.MaybeCompact(context.Background(), id)

	if pr.count() != 1 {
		t.Fatalf("re-entrant trigger must forward /compact once, got %d", pr.count())
	}
}

func TestMaybeCompactUnknownSessionIsNoOp(t *testing.T) {
	reg, _ := newSession(t)
	pr := &fakePrompter{}
	tr := &fakeTranscript{}
	s := NewService(reg, pr, tr, fakeRanger{ok: true}, 0.85, 0)
	s.MaybeCompact(context.Background(), "does-not-exist")

	if pr.count() != 0 || len(tr.calls) != 0 {
		t.Fatal("unknown session should be a no-op")
	}
}
