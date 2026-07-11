package resume

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/rpc"
)

// fakeReader serves a flat record set with Store-like paging: it returns records
// whose CursorSeq is past the cursor, up to limit, in order.
type fakeReader struct {
	recs  []api.Event
	calls int
}

func (r *fakeReader) Read(_ api.StreamID, after uint64, limit int) ([]api.Event, uint64, error) {
	r.calls++
	var out []api.Event
	for _, e := range r.recs {
		if e.CursorSeq > after {
			out = append(out, e)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, 0, nil
}

// fakeMem returns a canned memory context (and records the params it saw).
type fakeMem struct {
	res  api.MemoryContextResult
	err  error
	seen api.MemoryContextParams
}

func (m *fakeMem) Context(_ context.Context, p api.MemoryContextParams) (api.MemoryContextResult, error) {
	m.seen = p
	return m.res, m.err
}

func msg(t *testing.T, seq uint64, text string) api.Event {
	t.Helper()
	b, err := json.Marshal(api.AgentMessageChunk{Text: text})
	if err != nil {
		t.Fatal(err)
	}
	return api.Event{Kind: api.KindEvent, Seq: seq, CursorSeq: seq, Type: "agent_message_chunk", Payload: b}
}

func codeOf(t *testing.T, err error) int {
	t.Helper()
	var e *rpc.Error
	if !errors.As(err, &e) {
		t.Fatalf("want *rpc.Error, got %T: %v", err, err)
	}
	return e.Code
}

func TestResumeSeedRequiresIDs(t *testing.T) {
	s := NewService(&fakeReader{}, &fakeMem{}, nil)
	if _, err := s.ResumeSeed(context.Background(), api.SessionResumeSeedParams{WorkspaceID: "ws"}); codeOf(t, err) != rpc.CodeInvalidParams {
		t.Error("missing session_id should be invalid params")
	}
	if _, err := s.ResumeSeed(context.Background(), api.SessionResumeSeedParams{SessionID: "s"}); codeOf(t, err) != rpc.CodeInvalidParams {
		t.Error("missing workspace_id should be invalid params")
	}
}

func TestResumeSeedCombinesHistoryAndMemory(t *testing.T) {
	reader := &fakeReader{recs: []api.Event{msg(t, 1, "did the first thing"), msg(t, 2, "then the second")}}
	mem := &fakeMem{res: api.MemoryContextResult{Text: "rule: keep changes small", Summarized: false}}
	s := NewService(reader, mem, nil)

	res, err := s.ResumeSeed(context.Background(), api.SessionResumeSeedParams{SessionID: "s", WorkspaceID: "ws", Budget: 4000})
	if err != nil {
		t.Fatal(err)
	}
	if res.SourceSessionID != "s" {
		t.Errorf("source = %q, want s", res.SourceSessionID)
	}
	if !strings.Contains(res.Text, "did the first thing") || !strings.Contains(res.Text, "then the second") {
		t.Errorf("seed missing prior transcript: %q", res.Text)
	}
	if !strings.Contains(res.Text, "rule: keep changes small") {
		t.Errorf("seed missing memory: %q", res.Text)
	}
	// Memory leads, then the prior session (steering-first convention).
	if strings.Index(res.Text, "rule: keep changes small") > strings.Index(res.Text, "did the first thing") {
		t.Errorf("memory should precede prior session in the seed: %q", res.Text)
	}
	if res.Summarized {
		t.Error("a within-budget seed should not report Summarized")
	}
}

func TestResumeSeedReportsSummarizedFromMemory(t *testing.T) {
	// Even when the assembled seed fits the budget verbatim, a memory portion that
	// was already condensed makes the whole seed a digest.
	reader := &fakeReader{recs: []api.Event{msg(t, 1, "short")}}
	mem := &fakeMem{res: api.MemoryContextResult{Text: "condensed rules", Summarized: true}}
	s := NewService(reader, mem, nil)

	res, err := s.ResumeSeed(context.Background(), api.SessionResumeSeedParams{SessionID: "s", WorkspaceID: "ws", Budget: 4000})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Summarized {
		t.Error("a pre-condensed memory portion should mark the seed Summarized")
	}
}

func summaryRec(t *testing.T, seq uint64, text string) api.Event {
	t.Helper()
	b, err := json.Marshal(api.ContextSummary{Text: text})
	if err != nil {
		t.Fatal(err)
	}
	// A summary record is served at from_seq with CursorSeq = to_seq.
	return api.Event{Kind: api.KindSummary, Seq: seq, CursorSeq: seq, Type: "summary", Payload: b}
}

func TestResumeSeedReportsSummarizedWhenHistoryHasSummary(t *testing.T) {
	// A prior transcript summary injects already-condensed context, so the seed is
	// a digest even when the assembled text fits the budget verbatim.
	reader := &fakeReader{recs: []api.Event{summaryRec(t, 1, "condensed earlier turns"), msg(t, 5, "recent turn")}}
	mem := &fakeMem{res: api.MemoryContextResult{Text: "rules", Summarized: false}}
	s := NewService(reader, mem, nil)

	res, err := s.ResumeSeed(context.Background(), api.SessionResumeSeedParams{SessionID: "s", WorkspaceID: "ws", Budget: 4000})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "condensed earlier turns") {
		t.Errorf("seed missing the prior summary text: %q", res.Text)
	}
	if !res.Summarized {
		t.Error("a seed that folded in a prior transcript summary must report Summarized")
	}
}

func TestResumeSeedTruncatesOverBudget(t *testing.T) {
	reader := &fakeReader{recs: []api.Event{msg(t, 1, strings.Repeat("history ", 200))}}
	mem := &fakeMem{res: api.MemoryContextResult{Text: "rules"}}
	s := NewService(reader, mem, nil)

	res, err := s.ResumeSeed(context.Background(), api.SessionResumeSeedParams{SessionID: "s", WorkspaceID: "ws", Budget: 60})
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(res.Text)); n > 60 {
		t.Errorf("seed is %d runes, over budget 60", n)
	}
	if !res.Summarized {
		t.Error("an over-budget seed should report Summarized")
	}
}

func TestResumeSeedEmptyWhenNothingToResume(t *testing.T) {
	s := NewService(&fakeReader{}, &fakeMem{}, nil)
	res, err := s.ResumeSeed(context.Background(), api.SessionResumeSeedParams{SessionID: "s", WorkspaceID: "ws"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "" {
		t.Errorf("no history + no memory should yield an empty seed, got %q", res.Text)
	}
	if res.SourceSessionID != "s" {
		t.Errorf("source session should still be echoed, got %q", res.SourceSessionID)
	}
}

func TestResumeSeedPropagatesMemoryError(t *testing.T) {
	mem := &fakeMem{err: &rpc.Error{Code: rpc.CodeInternalError, Message: "memory down"}}
	s := NewService(&fakeReader{recs: []api.Event{msg(t, 1, "x")}}, mem, nil)
	if _, err := s.ResumeSeed(context.Background(), api.SessionResumeSeedParams{SessionID: "s", WorkspaceID: "ws"}); codeOf(t, err) != rpc.CodeInternalError {
		t.Error("a memory failure should propagate")
	}
}

func TestResumeSeedDoesNotBudgetMemoryToSeedBudget(t *testing.T) {
	// The memory assembly is asked for its own default budget (0), not the seed
	// budget, so the memory portion cannot consume the whole seed.
	reader := &fakeReader{recs: []api.Event{msg(t, 1, "x")}}
	mem := &fakeMem{res: api.MemoryContextResult{Text: "rules"}}
	s := NewService(reader, mem, nil)
	if _, err := s.ResumeSeed(context.Background(), api.SessionResumeSeedParams{SessionID: "s", WorkspaceID: "ws", Budget: 50}); err != nil {
		t.Fatal(err)
	}
	if mem.seen.Budget != 0 {
		t.Errorf("memory.Context Budget = %d, want 0 (its own default)", mem.seen.Budget)
	}
	if mem.seen.WorkspaceID != "ws" {
		t.Errorf("memory.Context WorkspaceID = %q, want ws", mem.seen.WorkspaceID)
	}
}

func TestReadAllPagesToTail(t *testing.T) {
	// More records than one batch forces paging; readAll must gather them all
	// without looping forever.
	var recs []api.Event
	for i := uint64(1); i <= readBatch+3; i++ {
		recs = append(recs, msg(t, i, "line"))
	}
	reader := &fakeReader{recs: recs}
	got, err := readAll(reader, api.SessionStream("s"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(recs) {
		t.Errorf("readAll gathered %d records, want %d", len(got), len(recs))
	}
	if reader.calls < 2 {
		t.Errorf("expected paging (>=2 reads), got %d", reader.calls)
	}
}
