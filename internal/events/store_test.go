package events

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dusto/tend/api"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	log, err := OpenLog(filepath.Join(t.TempDir(), "events.log"))
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return NewStore(log)
}

func TestStorePublishAssignsSeqAndPersists(t *testing.T) {
	s := newStore(t)
	for want := uint64(1); want <= 3; want++ {
		ev, err := s.Publish(api.Event{StreamID: "session:a", Type: "x"})
		if err != nil {
			t.Fatalf("Publish: %v", err)
		}
		if ev.Seq != want || ev.CursorSeq != want {
			t.Fatalf("Publish returned %+v, want seq %d", ev, want)
		}
	}
	if hw := s.HighWater("session:a"); hw != 3 {
		t.Errorf("HighWater = %d, want 3", hw)
	}
	got, _, _ := s.Read("session:a", 0, 10)
	if !equal(seqs(got), []uint64{1, 2, 3}) {
		t.Errorf("Read = %v, want [1 2 3]", seqs(got))
	}
}

func TestStoreSubscribeWakesOnPublish(t *testing.T) {
	s := newStore(t)
	sub := s.Subscribe("session:a")
	t.Cleanup(sub.Close)

	if _, err := s.Publish(api.Event{StreamID: "session:a"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sub.Wake():
	case <-time.After(time.Second):
		t.Fatal("no wake after publish")
	}
}

func TestStoreWakeOnlyForOwnStream(t *testing.T) {
	s := newStore(t)
	sub := s.Subscribe("session:a")
	t.Cleanup(sub.Close)

	if _, err := s.Publish(api.Event{StreamID: "session:b"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sub.Wake():
		t.Fatal("woke for another stream's publish")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestStoreCloseUnblocksAndUnregisters(t *testing.T) {
	s := newStore(t)
	sub := s.Subscribe("session:a")
	if n := s.subscriberCount("session:a"); n != 1 {
		t.Fatalf("subscriberCount = %d, want 1", n)
	}
	sub.Close()
	sub.Close() // idempotent
	select {
	case <-sub.Stop():
	case <-time.After(time.Second):
		t.Fatal("Stop not closed after Close")
	}
	if n := s.subscriberCount("session:a"); n != 0 {
		t.Errorf("subscriberCount = %d after Close, want 0", n)
	}
}

// TestStoreConcurrentMultiStream publishes concurrently across many streams and
// checks each stream's log stays a contiguous, monotonic 1..N (independent
// sequences, no cross-stream interference).
func TestStoreConcurrentMultiStream(t *testing.T) {
	s := newStore(t)
	const streams = 8
	const perStream = 200

	var wg sync.WaitGroup
	for n := range streams {
		wg.Go(func() {
			id := api.StreamID(fmt.Sprintf("session:%d", n))
			for range perStream {
				if _, err := s.Publish(api.Event{StreamID: id}); err != nil {
					t.Errorf("publish: %v", err)
					return
				}
			}
		})
	}
	wg.Wait()

	for n := range streams {
		id := api.StreamID(fmt.Sprintf("session:%d", n))
		got, _, _ := s.Read(id, 0, perStream+10)
		want := make([]uint64, perStream)
		for i := range want {
			want[i] = uint64(i + 1)
		}
		if !equal(seqs(got), want) {
			t.Fatalf("stream %d not contiguous 1..%d: got %d records", n, perStream, len(got))
		}
	}
}
