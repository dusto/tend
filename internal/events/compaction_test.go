package events

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/dusto/tend/api"
)

func compactStore(t *testing.T, retention uint64) *Store {
	t.Helper()
	log, err := OpenLog(filepath.Join(t.TempDir(), "events.log"))
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	s := NewStore(log)
	s.retention = retention
	return s
}

// readStream reads streamID from after and returns the record seqs in order and
// whether the cursor landed in a compacted range.
func readStream(t *testing.T, s *Store, stream api.StreamID, after uint64) ([]uint64, uint64) {
	t.Helper()
	recs, cf, err := s.Read(stream, after, 1<<30)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return seqs(recs), cf
}

func TestCompactReplayAcrossCursorPositions(t *testing.T) {
	s := compactStore(t, 2) // keep the last 2 raw records
	stream := api.StreamID("session:a")
	for range 10 {
		if _, err := s.Publish(api.Event{StreamID: stream, Scope: api.ScopeSession, Type: "tool_call"}); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}
	// Compact a completed range [3,7] (beyond the retention window: 8,9,10 remain).
	if err := s.Compact(stream, api.ScopeSession, 3, 7, nil); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	cases := []struct {
		name          string
		after         uint64
		wantSeqs      []uint64
		wantCompacted uint64
	}{
		// Before the range: raw 1,2, then the summary (served at 3), then 8,9,10.
		{"from zero", 0, []uint64{1, 2, 3, 8, 9, 10}, 0},
		// Just before the range: summary in order, then tail.
		{"just before range", 2, []uint64{3, 8, 9, 10}, 0},
		// At from_seq (inside [from,to)): cursor_compacted at the boundary.
		{"at from_seq", 3, []uint64{3, 8, 9, 10}, 3},
		// Mid-range (inside): cursor_compacted at the boundary.
		{"mid range", 5, []uint64{3, 8, 9, 10}, 3},
		// At to_seq: range already consumed, replay continues normally at to+1.
		{"at to_seq", 7, []uint64{8, 9, 10}, 0},
		// After the range: normal replay.
		{"after range", 8, []uint64{9, 10}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			seqs, cf := readStream(t, s, stream, c.after)
			if !equal(seqs, c.wantSeqs) {
				t.Errorf("seqs = %v, want %v", seqs, c.wantSeqs)
			}
			if cf != c.wantCompacted {
				t.Errorf("compactedFrom = %d, want %d", cf, c.wantCompacted)
			}
		})
	}
}

func TestCompactRespectsRetentionWindow(t *testing.T) {
	s := compactStore(t, 3) // keep the last 3 raw records
	stream := api.StreamID("session:a")
	for range 10 {
		_, _ = s.Publish(api.Event{StreamID: stream, Scope: api.ScopeSession})
	}
	// tail=10, retention=3: a range ending at 8+ leaves < 3 raw records -> refused.
	if err := s.Compact(stream, api.ScopeSession, 1, 8, nil); !errors.Is(err, ErrWithinRetention) {
		t.Errorf("compacting into the retention window: err = %v, want ErrWithinRetention", err)
	}
	// Ending at 7 leaves 8,9,10 -> allowed.
	if err := s.Compact(stream, api.ScopeSession, 1, 7, nil); err != nil {
		t.Errorf("compacting beyond the window: %v", err)
	}
}

func TestCompactRejectsOverlap(t *testing.T) {
	s := compactStore(t, 0)
	stream := api.StreamID("session:a")
	for range 10 {
		_, _ = s.Publish(api.Event{StreamID: stream, Scope: api.ScopeSession})
	}
	if err := s.Compact(stream, api.ScopeSession, 2, 5, nil); err != nil {
		t.Fatalf("first compact: %v", err)
	}
	if err := s.Compact(stream, api.ScopeSession, 4, 8, nil); !errors.Is(err, ErrSummaryOverlap) {
		t.Errorf("overlapping compact: err = %v, want ErrSummaryOverlap", err)
	}
	// A disjoint later range is fine.
	if err := s.Compact(stream, api.ScopeSession, 6, 9, nil); err != nil {
		t.Errorf("disjoint compact: %v", err)
	}
}

func TestCompactKeepsSeqContiguousAndRawRetained(t *testing.T) {
	s := compactStore(t, 0)
	stream := api.StreamID("session:a")
	for range 5 {
		_, _ = s.Publish(api.Event{StreamID: stream, Scope: api.ScopeSession})
	}
	if err := s.Compact(stream, api.ScopeSession, 1, 5, nil); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	// seq is not renumbered: the next event is 6, and the tail advanced.
	ev, err := s.Publish(api.Event{StreamID: stream, Scope: api.ScopeSession})
	if err != nil {
		t.Fatalf("Publish after compact: %v", err)
	}
	if ev.Seq != 6 {
		t.Errorf("post-compaction seq = %d, want 6 (no reuse/renumber)", ev.Seq)
	}
	// Raw records are retained in the log: reopening rebuilds them (the summary
	// shadows them on replay, but they survive for audit).
	if hw := s.HighWater(stream); hw != 6 {
		t.Errorf("high water = %d, want 6", hw)
	}
}

func TestCompactSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.log")
	stream := api.StreamID("session:a")

	log, err := OpenLog(path)
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	s := NewStore(log)
	s.retention = 0
	for range 6 {
		_, _ = s.Publish(api.Event{StreamID: stream, Scope: api.ScopeSession})
	}
	if err := s.Compact(stream, api.ScopeSession, 2, 4, nil); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	_ = log.Close()

	// Reopen: the summary and its replay behavior are rebuilt from the log.
	log2, err := OpenLog(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = log2.Close() })
	s2 := NewStore(log2)
	seqs, cf := readStream(t, s2, stream, 0)
	if !equal(seqs, []uint64{1, 2, 5, 6}) || cf != 0 {
		t.Errorf("after reopen: seqs = %v, compactedFrom = %d, want [1 2 5 6], 0", seqs, cf)
	}
}
