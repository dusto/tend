// Package events implements the multiplexed per-stream event store: a durable
// append-only log that is the sequence authority, plus live notification so a
// subscriber can tail one stream — replaying history then following live
// appends — with ordering and contiguity guaranteed within a stream and nothing
// assumed across streams.
package events

import (
	"sync"

	"github.com/dusto/tend/api"
)

// Store is the integrated event store. Publish appends an event to the durable
// log (assigning its per-stream seq) and wakes the stream's subscribers; a
// subscriber reads the log forward from its cursor, so replay and live delivery
// are a single path and live events are durable and seq-continuous. A Store is
// safe for concurrent use.
type Store struct {
	log *Log

	mu   sync.Mutex
	subs map[api.StreamID]map[*Sub]struct{}
}

// NewStore returns a Store backed by log.
func NewStore(log *Log) *Store {
	return &Store{log: log, subs: make(map[api.StreamID]map[*Sub]struct{})}
}

// Publish appends ev to the log (stamping its seq, kind, cursor, and ts) and
// wakes the stream's subscribers. It returns the stamped event.
func (s *Store) Publish(ev api.Event) (api.Event, error) {
	stamped, err := s.log.Append(ev)
	if err != nil {
		return api.Event{}, err
	}
	s.mu.Lock()
	for sub := range s.subs[stamped.StreamID] {
		sub.signal()
	}
	s.mu.Unlock()
	return stamped, nil
}

// Read returns up to limit records for streamID after the cursor, honoring
// compaction (see Log.Read).
func (s *Store) Read(streamID api.StreamID, after uint64, limit int) ([]api.Event, uint64, error) {
	return s.log.Read(streamID, after, limit)
}

// HighWater returns streamID's current tail.
func (s *Store) HighWater(streamID api.StreamID) uint64 {
	return s.log.HighWater(streamID)
}

// Subscribe registers a subscriber for streamID and returns a Sub whose Wake
// channel fires (coalescing) whenever the stream gains records. Close to
// unsubscribe. A subscriber reads the log itself; the Store holds no per-stream
// event buffer, so a slow subscriber never blocks a publisher.
func (s *Store) Subscribe(streamID api.StreamID) *Sub {
	sub := &Sub{
		store:    s,
		streamID: streamID,
		wake:     make(chan struct{}, 1),
		stop:     make(chan struct{}),
	}
	s.mu.Lock()
	set := s.subs[streamID]
	if set == nil {
		set = make(map[*Sub]struct{})
		s.subs[streamID] = set
	}
	set[sub] = struct{}{}
	s.mu.Unlock()
	return sub
}

// subscriberCount reports how many subscribers are registered for streamID.
func (s *Store) subscriberCount(streamID api.StreamID) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs[streamID])
}

func (s *Store) remove(sub *Sub) {
	s.mu.Lock()
	if set := s.subs[sub.streamID]; set != nil {
		delete(set, sub)
		if len(set) == 0 {
			delete(s.subs, sub.streamID)
		}
	}
	s.mu.Unlock()
}

// Sub is a live subscription to one stream. It carries no events itself: Wake
// signals that new records are available, and the reader reads them from the
// Store. Wake is coalescing (a pending wake is not duplicated).
type Sub struct {
	store    *Store
	streamID api.StreamID
	wake     chan struct{}
	stop     chan struct{}
	once     sync.Once
}

// Wake fires when the stream may have new records. It is coalescing, so a single
// receive may cover several appends; always read up to the current tail.
func (sub *Sub) Wake() <-chan struct{} { return sub.wake }

// Stop is closed when the subscription is closed, unblocking a waiting reader.
func (sub *Sub) Stop() <-chan struct{} { return sub.stop }

// Close unsubscribes and unblocks any reader waiting on Wake. It is idempotent.
func (sub *Sub) Close() {
	sub.once.Do(func() {
		close(sub.stop)
		sub.store.remove(sub)
	})
}

// signal delivers a coalescing wake (non-blocking).
func (sub *Sub) signal() {
	select {
	case sub.wake <- struct{}{}:
	default:
	}
}
