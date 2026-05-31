package events

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/dusto/tend/api"
)

func recv(t *testing.T, sub *Subscription) api.Event {
	t.Helper()
	select {
	case ev := <-sub.C:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
		return api.Event{}
	}
}

func TestPublishStampsAndSequences(t *testing.T) {
	b := NewBus()
	sub := b.Subscribe("session:s1")
	t.Cleanup(sub.Close)

	const n = 5
	for range n {
		b.Publish(api.Event{StreamID: "session:s1", Scope: api.ScopeSession, Type: "tool_call"})
	}
	for i := uint64(1); i <= n; i++ {
		ev := recv(t, sub)
		if ev.Seq != i {
			t.Fatalf("Seq = %d, want %d", ev.Seq, i)
		}
		if ev.CursorSeq != ev.Seq {
			t.Errorf("CursorSeq = %d, want == Seq %d", ev.CursorSeq, ev.Seq)
		}
		if ev.Kind != api.KindEvent {
			t.Errorf("Kind = %q, want event", ev.Kind)
		}
		if ev.TS.IsZero() {
			t.Error("TS not stamped")
		}
	}
}

func TestPublishReturnsStampedEvent(t *testing.T) {
	b := NewBus()
	got := b.Publish(api.Event{StreamID: "session:s1", Type: "turn_end"})
	if got.Seq != 1 || got.CursorSeq != 1 || got.Kind != api.KindEvent {
		t.Fatalf("returned event = %+v", got)
	}
}

func TestIndependentStreamsHaveIndependentSeqs(t *testing.T) {
	b := NewBus()
	subA := b.Subscribe("session:a")
	subB := b.Subscribe("session:b")
	t.Cleanup(subA.Close)
	t.Cleanup(subB.Close)

	b.Publish(api.Event{StreamID: "session:a"})
	b.Publish(api.Event{StreamID: "session:b"})
	b.Publish(api.Event{StreamID: "session:a"})

	if ev := recv(t, subA); ev.Seq != 1 {
		t.Fatalf("A first Seq = %d, want 1", ev.Seq)
	}
	if ev := recv(t, subA); ev.Seq != 2 {
		t.Fatalf("A second Seq = %d, want 2", ev.Seq)
	}
	if ev := recv(t, subB); ev.Seq != 1 {
		t.Fatalf("B first Seq = %d, want 1 (independent of A)", ev.Seq)
	}
}

func TestSubscribeOnlyReceivesItsStream(t *testing.T) {
	b := NewBus()
	sub := b.Subscribe("session:a")
	t.Cleanup(sub.Close)

	b.Publish(api.Event{StreamID: "session:b"}) // other stream
	b.Publish(api.Event{StreamID: "session:a", Type: "mine"})

	ev := recv(t, sub)
	if ev.StreamID != "session:a" || ev.Type != "mine" {
		t.Fatalf("received %+v, want only session:a event", ev)
	}
	select {
	case ev := <-sub.C:
		t.Fatalf("unexpected extra event %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSeqAdvancesWithoutSubscribers(t *testing.T) {
	b := NewBus()
	b.Publish(api.Event{StreamID: "session:a"})
	b.Publish(api.Event{StreamID: "session:a"})

	sub := b.Subscribe("session:a")
	t.Cleanup(sub.Close)
	got := b.Publish(api.Event{StreamID: "session:a"})
	if got.Seq != 3 {
		t.Fatalf("Seq = %d, want 3 (advanced while unsubscribed)", got.Seq)
	}
	if ev := recv(t, sub); ev.Seq != 3 {
		t.Fatalf("subscriber Seq = %d, want 3", ev.Seq)
	}
}

func TestCloseStopsDelivery(t *testing.T) {
	b := NewBus()
	sub := b.Subscribe("session:a")
	sub.Close()
	sub.Close() // idempotent

	// Publishing after close must not block and must not be observed.
	b.Publish(api.Event{StreamID: "session:a"})
	select {
	case _, ok := <-sub.C:
		if ok {
			t.Fatal("received event after Close")
		}
	case <-time.After(50 * time.Millisecond):
	}
}

// TestConcurrentMultiStream is the core guarantee: under concurrent publishes
// across many streams, each stream's subscriber sees a contiguous, monotonic
// 1..N sequence carrying only its own StreamID, with no cross-stream
// interleaving.
func TestConcurrentMultiStream(t *testing.T) {
	b := NewBus()
	const streams = 8
	const perStream = 200

	subs := make([]*Subscription, streams)
	ids := make([]api.StreamID, streams)
	for s := range streams {
		ids[s] = api.StreamID(fmt.Sprintf("session:%d", s))
		subs[s] = b.Subscribe(ids[s])
		t.Cleanup(subs[s].Close)
	}

	var pub sync.WaitGroup
	for s := range streams {
		pub.Go(func() {
			for range perStream {
				b.Publish(api.Event{StreamID: ids[s], Scope: api.ScopeSession})
			}
		})
	}

	// Drain each stream concurrently, asserting contiguity and stream identity.
	var drain sync.WaitGroup
	errs := make(chan error, streams)
	for s := range streams {
		drain.Go(func() {
			for want := uint64(1); want <= perStream; want++ {
				ev := recv(t, subs[s])
				if ev.StreamID != ids[s] {
					errs <- fmt.Errorf("stream %d got foreign StreamID %q", s, ev.StreamID)
					return
				}
				if ev.Seq != want {
					errs <- fmt.Errorf("stream %d Seq = %d, want %d", s, ev.Seq, want)
					return
				}
			}
		})
	}

	pub.Wait()
	drain.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
