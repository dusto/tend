package events

import (
	"sync"

	"github.com/dusto/tend/api"
)

// Deduper implements the client-side dedup that makes at-least-once delivery
// safe. Pushed events are not acked per message and a client advances its cursor
// only after processing, so a reconnect replays (last_seq, tail] and may
// redeliver events the client already handled. Clients dedup by
// (stream_id, seq, kind): a redelivered record with the same key is dropped.
//
// A summary record occupies its range's from_seq but has kind=summary, so it is
// a distinct key from the raw kind=event record that held that seq — dedup never
// drops a summary against a previously-seen raw event at the same seq, and
// applying the summary supersedes that range.
//
// This is the reference implementation (the Lua client mirrors it); the daemon
// itself does not dedup. It is safe for concurrent use.
type Deduper struct {
	mu   sync.Mutex
	seen map[dedupKey]struct{}
}

type dedupKey struct {
	stream api.StreamID
	seq    uint64
	kind   api.EventKind
}

// NewDeduper returns an empty Deduper.
func NewDeduper() *Deduper {
	return &Deduper{seen: make(map[dedupKey]struct{})}
}

// Fresh records ev and reports whether it is new (should be processed). A second
// record with the same (stream_id, seq, kind) returns false.
func (d *Deduper) Fresh(ev api.Event) bool {
	k := dedupKey{stream: ev.StreamID, seq: ev.Seq, kind: ev.Kind}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.seen[k]; ok {
		return false
	}
	d.seen[k] = struct{}{}
	return true
}

// ForgetThrough drops remembered keys for a stream at or below seq, bounding
// memory once a client has persisted its cursor past seq (those records can no
// longer be usefully redelivered). A summary that ends at seq is retained, since
// it may still arrive against an older cursor and must supersede the range.
func (d *Deduper) ForgetThrough(stream api.StreamID, seq uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for k := range d.seen {
		if k.stream == stream && k.seq <= seq && k.kind == api.KindEvent {
			delete(d.seen, k)
		}
	}
}
