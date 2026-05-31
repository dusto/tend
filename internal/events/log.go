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

// ErrNonContiguous is returned by Append when an event's Seq is not the next
// sequence number for its stream. The log is contiguous within a stream.
var ErrNonContiguous = errors.New("events: non-contiguous append")

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
func (l *Log) Append(ev api.Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	expect := uint64(1)
	if si := l.index[ev.StreamID]; si != nil {
		expect = si.tail + 1
	}
	if ev.Seq != expect {
		return fmt.Errorf("%w: stream %q seq %d, want %d", ErrNonContiguous, ev.StreamID, ev.Seq, expect)
	}

	ev.Kind = api.KindEvent
	ev.CursorSeq = ev.Seq
	if err := l.write(ev); err != nil {
		return err
	}
	l.indexRecord(ev)
	return nil
}

// AppendSummary records a compaction of [from, to] for a stream by appending a
// summary control record (kind=summary) served at from with CursorSeq=to. The
// raw records in the range are retained; replay serves the summary in their
// place. The range must lie within the stream's existing history.
func (l *Log) AppendSummary(streamID api.StreamID, scope api.EventScope, from, to uint64, payload json.RawMessage) error {
	l.mu.Lock()
	defer l.mu.Unlock()

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

// Replay returns the records to deliver for streamID in (lastSeq, tail], in seq
// order, honoring compaction: a summary is served at its from_seq in place of
// the raw records it subsumes. compactedFrom is non-zero when lastSeq fell
// inside a summarized range [from, to): the returned records begin with that
// summary (re-delivered) and compactedFrom is the resume boundary the caller
// reports as cursor_compacted. Returned events are copies safe to deliver.
func (l *Log) Replay(streamID api.StreamID, lastSeq uint64) (records []api.Event, compactedFrom uint64, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	si := l.index[streamID]
	if si == nil {
		return nil, 0, nil
	}

	for p := uint64(1); p <= si.tail; {
		if sum, ok := si.summary[p]; ok {
			to := sum.Summary.ToSeq
			switch {
			case lastSeq < p:
				records = append(records, sum) // not yet reached: deliver in order
			case lastSeq < to:
				records = append(records, sum) // inside the range: re-deliver
				compactedFrom = p
			}
			// lastSeq >= to: already consumed; skip the summary.
			p = to + 1
			continue
		}
		if ev, ok := si.raw[p]; ok && p > lastSeq {
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
