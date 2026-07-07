package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/dusto/tend/api"
)

// Log is a durable, append-only event log with a per-stream replay index.
// Records — raw events and compaction summary control records — are appended as
// newline-delimited JSON and never rewritten or deleted; raw records are
// retained for debug/audit even after a range is summarized. On open the log
// rebuilds its per-stream index by scanning the file, so a stream's high-water
// and replayable history survive a restart. A Log is safe for concurrent use.
type Log struct {
	mu    sync.Mutex
	f     *os.File
	index map[api.StreamID]*streamIndex
}

// streamIndex is one stream's replay index: its raw events by seq, its summary
// control records by the from_seq they are served at, and its high-water tail.
type streamIndex struct {
	raw     map[uint64]api.Event // kind=event, by Seq
	summary map[uint64]api.Event // kind=summary, by FromSeq (served at FromSeq)
	tail    uint64               // highest raw event Seq
}

// OpenLog opens (creating if needed) the append-only log at path and rebuilds
// its per-stream index from the existing records.
func OpenLog(path string) (*Log, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	l := &Log{f: f, index: make(map[api.StreamID]*streamIndex)}
	if err := l.load(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return l, nil
}

// Close closes the underlying file.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}

// load scans the whole file once and rebuilds the index.
func (l *Log) load() error {
	if _, err := l.f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	dec := json.NewDecoder(l.f)
	for {
		var ev api.Event
		if err := dec.Decode(&ev); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("events: reading log: %w", err)
		}
		l.indexRecord(ev)
	}
}

// Append durably writes a raw event and indexes it. ev.Seq must be the next
// sequence number for its stream (1 for the first), matching the contiguous
// seq the bus assigns; otherwise it returns ErrNonContiguous.
// Append assigns ev the next per-stream sequence number, stamps it as a
// kind=event record (CursorSeq == Seq, TS defaulted), durably writes it, and
// returns the stamped event. The log is the sequence authority for a stream, so
// across a restart seq continues from the reloaded high-water.
func (l *Log) Append(ev api.Event) (api.Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	var tail uint64
	if si := l.index[ev.StreamID]; si != nil {
		tail = si.tail
	}
	ev.Kind = api.KindEvent
	ev.Seq = tail + 1
	ev.CursorSeq = ev.Seq
	if ev.TS.IsZero() {
		ev.TS = time.Now()
	}
	if err := l.write(ev); err != nil {
		return api.Event{}, err
	}
	l.indexRecord(ev)
	return ev, nil
}

// AppendSummary records a compaction of [from, to] for a stream by appending a
// summary control record (kind=summary) served at from with CursorSeq=to. The
// raw records in the range are retained; replay serves the summary in their
// place. The range must lie within the stream's existing history.
func (l *Log) AppendSummary(streamID api.StreamID, scope api.EventScope, from, to uint64, payload json.RawMessage) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.appendSummaryLocked(streamID, scope, from, to, payload)
}

// Compaction errors.
var (
	// ErrWithinRetention reports that a range is too recent to compact: at least
	// the retention window of most-recent raw records is kept for exact replay.
	ErrWithinRetention = errors.New("events: range is within the retention window")
	// ErrSummaryOverlap reports that a range overlaps an existing summary.
	ErrSummaryOverlap = errors.New("events: range overlaps an existing summary")
)

// Compact summarizes [from, to] only when it lies beyond the retention window —
// at least retention most-recent raw records remain — and does not overlap an
// existing summary. seq is never reused or renumbered: the raw records stay in
// the log and the stream's tail is unchanged, so new events keep counting up.
func (l *Log) Compact(streamID api.StreamID, scope api.EventScope, from, to, retention uint64, payload json.RawMessage) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	si := l.index[streamID]
	if from == 0 || from > to {
		return fmt.Errorf("events: invalid summary range [%d,%d]", from, to)
	}
	if si == nil || to > si.tail {
		return fmt.Errorf("events: summary range [%d,%d] exceeds stream %q history", from, to, streamID)
	}
	// Keep the most-recent retention raw records uncompacted for exact replay.
	if si.tail < to+retention {
		return ErrWithinRetention
	}
	for f, sum := range si.summary {
		if from <= sum.Summary.ToSeq && f <= to { // ranges intersect
			return ErrSummaryOverlap
		}
	}
	return l.appendSummaryLocked(streamID, scope, from, to, payload)
}

// appendSummaryLocked writes and indexes a summary record. The caller holds the
// lock; it validates the range against the stream's history.
func (l *Log) appendSummaryLocked(streamID api.StreamID, scope api.EventScope, from, to uint64, payload json.RawMessage) error {
	si := l.index[streamID]
	if from == 0 || from > to {
		return fmt.Errorf("events: invalid summary range [%d,%d]", from, to)
	}
	if si == nil || to > si.tail {
		return fmt.Errorf("events: summary range [%d,%d] exceeds stream %q history", from, to, streamID)
	}

	ev := api.Event{
		StreamID:  streamID,
		Scope:     scope,
		Kind:      api.KindSummary,
		Seq:       from,
		CursorSeq: to,
		Type:      "summary",
		TS:        time.Now(),
		Payload:   payload,
		Summary:   &api.SummaryInfo{FromSeq: from, ToSeq: to},
	}
	if err := l.write(ev); err != nil {
		return err
	}
	l.indexRecord(ev)
	return nil
}

// HighWater returns the highest raw event seq for streamID, or 0 if the stream
// has no events.
func (l *Log) HighWater(streamID api.StreamID) uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	if si := l.index[streamID]; si != nil {
		return si.tail
	}
	return 0
}

// LastSummaryEnd returns the highest to_seq of any summary on streamID, or 0
// when the stream has no summaries. It marks the end of the already-compacted
// prefix, so the next compactable range begins at LastSummaryEnd+1.
func (l *Log) LastSummaryEnd(streamID api.StreamID) uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	si := l.index[streamID]
	if si == nil {
		return 0
	}
	var end uint64
	for _, sum := range si.summary {
		if sum.Summary.ToSeq > end {
			end = sum.Summary.ToSeq
		}
	}
	return end
}

// Read returns up to limit records to deliver for streamID after the cursor
// after, in seq order, honoring compaction: a summary is served at its from_seq
// in place of the raw records it subsumes. compactedFrom is non-zero when after
// fell inside a summarized range [from, to): the returned records begin with
// that summary and compactedFrom is the resume boundary the caller reports as
// cursor_compacted. A non-positive limit returns no records. Returned events are
// copies safe to deliver; advance the cursor by each record's CursorSeq.
func (l *Log) Read(streamID api.StreamID, after uint64, limit int) (records []api.Event, compactedFrom uint64, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	si := l.index[streamID]
	if si == nil || limit <= 0 {
		return nil, 0, nil
	}

	for p := uint64(1); p <= si.tail && len(records) < limit; {
		if sum, ok := si.summary[p]; ok {
			to := sum.Summary.ToSeq
			switch {
			case after < p:
				records = append(records, sum) // not yet reached: deliver in order
			case after < to:
				records = append(records, sum) // inside the range: re-deliver
				compactedFrom = p
			}
			// after >= to: already consumed; skip the summary.
			p = to + 1
			continue
		}
		if ev, ok := si.raw[p]; ok && p > after {
			records = append(records, ev)
		}
		p++
	}
	return records, compactedFrom, nil
}

func (l *Log) write(ev api.Event) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if _, err := l.f.Write(append(b, '\n')); err != nil {
		return err
	}
	return l.f.Sync()
}

func (l *Log) indexRecord(ev api.Event) {
	si := l.index[ev.StreamID]
	if si == nil {
		si = &streamIndex{raw: make(map[uint64]api.Event), summary: make(map[uint64]api.Event)}
		l.index[ev.StreamID] = si
	}
	if ev.Kind == api.KindSummary {
		si.summary[ev.Seq] = ev
		return
	}
	si.raw[ev.Seq] = ev
	si.tail = max(si.tail, ev.Seq)
}
