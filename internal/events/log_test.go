package events

import (
	"encoding/json"
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

// appendN appends n raw events to streamID and returns the seq of the last one.
func appendN(t *testing.T, l *Log, streamID api.StreamID, n int) uint64 {
	t.Helper()
	var last uint64
	for range n {
		ev, err := l.Append(api.Event{StreamID: streamID, Scope: api.ScopeSession, Type: "tool_call"})
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		last = ev.Seq
	}
	return last
}

// readAll returns every record for streamID after the cursor.
func readAll(t *testing.T, l *Log, streamID api.StreamID, after uint64) ([]api.Event, uint64) {
	t.Helper()
	recs, compactedFrom, err := l.Read(streamID, after, 1<<30)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return recs, compactedFrom
}

func seqs(records []api.Event) []uint64 {
	out := make([]uint64, len(records))
	for i, r := range records {
		out[i] = r.Seq
	}
	return out
}

func TestAppendAssignsContiguousSeq(t *testing.T) {
	l, _ := openLog(t)
	for want := uint64(1); want <= 3; want++ {
		ev, err := l.Append(api.Event{StreamID: "session:a"})
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		if ev.Seq != want || ev.CursorSeq != want || ev.Kind != api.KindEvent {
			t.Fatalf("Append returned %+v, want seq %d kind event", ev, want)
		}
		if ev.TS.IsZero() {
			t.Error("TS not stamped")
		}
	}
	// Streams sequence independently.
	if ev, _ := l.Append(api.Event{StreamID: "session:b"}); ev.Seq != 1 {
		t.Errorf("independent stream first Seq = %d, want 1", ev.Seq)
	}
}

func TestReadRange(t *testing.T) {
	l, _ := openLog(t)
	appendN(t, l, "session:a", 5)

	got, compacted := readAll(t, l, "session:a", 2)
	if compacted != 0 {
		t.Errorf("compactedFrom = %d, want 0", compacted)
	}
	if want := []uint64{3, 4, 5}; !equal(seqs(got), want) {
		t.Errorf("Read(after=2) seqs = %v, want %v", seqs(got), want)
	}

	if got, _ := readAll(t, l, "session:a", 5); len(got) != 0 {
		t.Errorf("Read(after=tail) = %v, want empty", seqs(got))
	}
	if got, _, _ := l.Read("session:none", 0, 10); got != nil {
		t.Errorf("Read(unknown) = %v, want nil", got)
	}
}

func TestReadLimit(t *testing.T) {
	l, _ := openLog(t)
	appendN(t, l, "session:a", 10)

	got, _, err := l.Read("session:a", 0, 4)
	if err != nil {
		t.Fatal(err)
	}
	if want := []uint64{1, 2, 3, 4}; !equal(seqs(got), want) {
		t.Errorf("Read(limit=4) = %v, want %v", seqs(got), want)
	}
	// Continuing from the last cursor returns the next batch.
	got, _, _ = l.Read("session:a", 4, 4)
	if want := []uint64{5, 6, 7, 8}; !equal(seqs(got), want) {
		t.Errorf("Read(after=4, limit=4) = %v, want %v", seqs(got), want)
	}
	if n, _, _ := l.Read("session:a", 0, 0); n != nil {
		t.Errorf("Read(limit=0) = %v, want nil", n)
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
	got, _ := readAll(t, reopened, "session:a", 1)
	if want := []uint64{2, 3, 4}; !equal(seqs(got), want) {
		t.Errorf("after restart Read(a, after=1) = %v, want %v", seqs(got), want)
	}
	// Appending continues from the persisted high-water (seq authority survives).
	if ev, err := reopened.Append(api.Event{StreamID: "session:a"}); err != nil || ev.Seq != 5 {
		t.Errorf("append after restart = %+v, %v; want seq 5", ev, err)
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
	got, comp := readAll(t, l, "session:a", 2)
	assertSummaryReplay(t, got, comp, []uint64{3, 8, 9, 10}, 0)
	if got[0].Kind != api.KindSummary || got[0].CursorSeq != 7 {
		t.Errorf("first record = %+v, want summary with CursorSeq 7", got[0])
	}

	// inside the range (from <= after < to): re-deliver summary, compactedFrom=3
	got, comp = readAll(t, l, "session:a", 4)
	assertSummaryReplay(t, got, comp, []uint64{3, 8, 9, 10}, 3)

	// after == from also falls inside the range (ack is at to_seq).
	got, comp = readAll(t, l, "session:a", 3)
	assertSummaryReplay(t, got, comp, []uint64{3, 8, 9, 10}, 3)

	// after >= to: summary already consumed, resume after it.
	got, comp = readAll(t, l, "session:a", 7)
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
