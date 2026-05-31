package events

import (
	"sync"
	"time"

	"github.com/dusto/tend/api"
)

// subBuffer is the per-subscription channel capacity. A subscriber that falls
// this far behind blocks further publishes on its stream (only that stream);
// overflow/backpressure policy is handled separately.
const subBuffer = 256

// Bus is the in-process, multiplexed event bus. Each logical stream has its own
// contiguous, monotonic sequence starting at 1, and publishes fan out only to
// that stream's subscribers. Ordering and contiguity hold within a single
// stream; nothing is guaranteed across streams. A Bus is safe for concurrent
// use.
type Bus struct {
	mu      sync.Mutex
	streams map[api.StreamID]*streamState
	nextSub uint64
}

// streamState holds one stream's sequence counter and current subscribers,
// guarded by its own mutex so streams publish independently.
type streamState struct {
	mu   sync.Mutex
	seq  uint64
	subs map[uint64]*Subscription
}

// NewBus returns an empty Bus.
func NewBus() *Bus {
	return &Bus{streams: make(map[api.StreamID]*streamState)}
}

// Publish assigns ev the next sequence number for ev.StreamID, stamps it as a
// kind=event record (CursorSeq == Seq), defaults TS to now when zero, fans it
// out to the stream's current subscribers in order, and returns the stamped
// event. Sequences advance whether or not the stream has subscribers.
func (b *Bus) Publish(ev api.Event) api.Event {
	st := b.streamFor(ev.StreamID)

	st.mu.Lock()
	defer st.mu.Unlock()

	st.seq++
	ev.Kind = api.KindEvent
	ev.Seq = st.seq
	ev.CursorSeq = st.seq
	if ev.TS.IsZero() {
		ev.TS = time.Now()
	}

	// Send in order while holding st.mu so concurrent publishers to the same
	// stream cannot interleave their deliveries. A closed subscriber is skipped
	// at once via its done channel, so Close never deadlocks against a send.
	for _, sub := range st.subs {
		select {
		case sub.ch <- ev:
		case <-sub.done:
		}
	}
	return ev
}

// Subscribe registers a subscriber for streamID and returns a Subscription that
// delivers events published to that stream after this call, in sequence order.
// Call Close to stop delivery.
func (b *Bus) Subscribe(streamID api.StreamID) *Subscription {
	st := b.streamFor(streamID)

	b.mu.Lock()
	b.nextSub++
	id := b.nextSub
	b.mu.Unlock()

	ch := make(chan api.Event, subBuffer)
	sub := &Subscription{
		C:        ch,
		ch:       ch,
		done:     make(chan struct{}),
		bus:      b,
		streamID: streamID,
		id:       id,
	}

	st.mu.Lock()
	st.subs[id] = sub
	st.mu.Unlock()
	return sub
}

// streamFor returns the state for streamID, creating it on first use.
func (b *Bus) streamFor(streamID api.StreamID) *streamState {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.streams[streamID]
	if st == nil {
		st = &streamState{subs: make(map[uint64]*Subscription)}
		b.streams[streamID] = st
	}
	return st
}

// Subscription is a live feed of one stream's events. Receive from C; the
// channel is never closed, so use Close (and stop receiving) to unsubscribe.
type Subscription struct {
	// C delivers events for the subscribed stream in sequence order.
	C <-chan api.Event

	ch       chan api.Event
	done     chan struct{}
	bus      *Bus
	streamID api.StreamID
	id       uint64
	once     sync.Once
}

// Close stops delivery to the subscription and removes it from the bus. It is
// idempotent and unblocks any in-flight publish that was waiting to send to
// this subscriber.
func (s *Subscription) Close() {
	s.once.Do(func() {
		close(s.done)
		st := s.bus.streamFor(s.streamID)
		st.mu.Lock()
		delete(st.subs, s.id)
		st.mu.Unlock()
	})
}
