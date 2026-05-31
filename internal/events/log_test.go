package events

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/dusto/tend/api"
)

func openLog(t *testing.T) (*Log, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.log")
	l, err := OpenLog(path)
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, path
}

// appendN appends n raw events to streamID starting at the stream's next seq.
func appendN(t *testing.T, l *Log, streamID api.StreamID, n int) {
	t.Helper()
	start := l.HighWater(streamID) + 1
	for i := range uint64(n) {
		seq := start + i
		if err := l.Append(api.Event{StreamID: streamID, Scope: api.ScopeSession, Type: "tool_call", Seq: seq}); err != nil {
			t.Fatalf("Append seq %d: %v", seq, err)
		}
	}
}

func seqs(records []api.Event) []uint64 {
	out := make([]uint64, len(records))
	for i, r := range records {
		out[i] = r.Seq
	}
	return out
}

func TestAppendContiguityEnforced(t *testing.T) {
	l, _ := openLog(t)
	if err := l.Append(api.Event{StreamID: "session:a", Seq: 1}); err != nil {
		t.Fatalf("first append: %v", err)
	}
	if err := l.Append(api.Event{StreamID: "session:a", Seq: 3}); !errors.Is(err, ErrNonContiguous) {
		t.Fatalf("gap append err = %v, want ErrNonContiguous", err)
	}
	// First event on a fresh stream must be seq 1.
	if err := l.Append(api.Event{StreamID: "session:b", Seq: 2}); !errors.Is(err, ErrNonContiguous) {
		t.Fatalf("non-1 first append err = %v, want ErrNonContiguous", err)
	}
}

func TestReplayRange(t *testing.T) {
	l, _ := openLog(t)
	appendN(t, l, "session:a", 5)

	got, compacted, err := l.Replay("session:a", 2)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if compacted != 0 {
		t.Errorf("compactedFrom = %d, want 0", compacted)
	}
	if want := []uint64{3, 4, 5}; !equal(seqs(got), want) {
		t.Errorf("Replay(2) seqs = %v, want %v", seqs(got), want)
	}

	// last_seq == tail yields nothing; an unknown stream yields nothing.
	if got, _, _ := l.Replay("session:a", 5); len(got) != 0 {
		t.Errorf("Replay(tail) = %v, want empty", seqs(got))
	}
	if got, _, _ := l.Replay("session:none", 0); got != nil {
		t.Errorf("Replay(unknown) = %v, want nil", got)
	}
}

func TestHighWater(t *testing.T) {
	l, _ := openLog(t)
	if hw := l.HighWater("session:a"); hw != 0 {
		t.Errorf("empty HighWater = %d, want 0", hw)
	}
	appendN(t, l, "session:a", 3)
	if hw := l.HighWater("session:a"); hw != 3 {
		t.Errorf("HighWater = %d, want 3", hw)
	}
}

func TestSurvivesRestart(t *testing.T) {
	l, path := openLog(t)
	appendN(t, l, "session:a", 4)
	appendN(t, l, "session:b", 2)
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := OpenLog(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	if hw := reopened.HighWater("session:a"); hw != 4 {
		t.Errorf("after restart HighWater(a) = %d, want 4", hw)
	}
	got, _, _ := reopened.Replay("session:a", 1)
	if want := []uint64{2, 3, 4}; !equal(seqs(got), want) {
		t.Errorf("after restart Replay(a,1) = %v, want %v", seqs(got), want)
	}
	// Appending continues from the persisted high-water.
	if err := reopened.Append(api.Event{StreamID: "session:a", Seq: 5}); err != nil {
		t.Errorf("append after restart: %v", err)
	}
}

func TestSummaryReplaySemantics(t *testing.T) {
	l, _ := openLog(t)
	appendN(t, l, "session:a", 10)
	// Compact [3,7] into a summary; raw 3..7 are retained but served as summary.
	if err := l.AppendSummary("session:a", api.ScopeSession, 3, 7, json.RawMessage(`{"text":"sum"}`)); err != nil {
		t.Fatalf("AppendSummary: %v", err)
	}

	// before the range: 1,2 already consumed -> summary(3) + 8,9,10
	got, comp, _ := l.Replay("session:a", 2)
	assertSummaryReplay(t, got, comp, []uint64{3, 8, 9, 10}, 0)
	if got[0].Kind != api.KindSummary || got[0].CursorSeq != 7 {
		t.Errorf("first record = %+v, want summary with CursorSeq 7", got[0])
	}

	// inside the range (from <= last < to): re-deliver summary, compactedFrom=3
	got, comp, _ = l.Replay("session:a", 4)
	assertSummaryReplay(t, got, comp, []uint64{3, 8, 9, 10}, 3)

	// last_seq == from also falls inside the range (ack is at to_seq).
	got, comp, _ = l.Replay("session:a", 3)
	assertSummaryReplay(t, got, comp, []uint64{3, 8, 9, 10}, 3)

	// last_seq >= to: summary already consumed, resume after it.
	got, comp, _ = l.Replay("session:a", 7)
	assertSummaryReplay(t, got, comp, []uint64{8, 9, 10}, 0)
}

func TestSummaryRangeValidated(t *testing.T) {
	l, _ := openLog(t)
	appendN(t, l, "session:a", 3)
	if err := l.AppendSummary("session:a", api.ScopeSession, 2, 9, nil); err == nil {
		t.Error("summary exceeding tail should fail")
	}
	if err := l.AppendSummary("session:a", api.ScopeSession, 0, 2, nil); err == nil {
		t.Error("summary with from=0 should fail")
	}
	if err := l.AppendSummary("session:none", api.ScopeSession, 1, 1, nil); err == nil {
		t.Error("summary on unknown stream should fail")
	}
}

func assertSummaryReplay(t *testing.T, got []api.Event, compacted uint64, wantSeqs []uint64, wantCompacted uint64) {
	t.Helper()
	if !equal(seqs(got), wantSeqs) {
		t.Errorf("replay seqs = %v, want %v", seqs(got), wantSeqs)
	}
	if compacted != wantCompacted {
		t.Errorf("compactedFrom = %d, want %d", compacted, wantCompacted)
	}
}

func equal(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
