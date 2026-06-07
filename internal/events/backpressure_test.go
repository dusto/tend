package events

import (
	"context"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/rpc"
)

// idleConn returns a real rpc.Conn whose peer is never read, used purely as the
// connection identity for the Pusher (delivery is mocked via the deliver seam).
func idleConn(t *testing.T) *rpc.Conn {
	t.Helper()
	a, b := net.Pipe()
	conn := rpc.NewConn(a, nil)
	t.Cleanup(func() { _ = conn.Close(); _ = b.Close() })
	return conn
}

func storeWithEvents(t *testing.T, stream api.StreamID, n int) *Store {
	t.Helper()
	log, err := OpenLog(filepath.Join(t.TempDir(), "events.log"))
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	store := NewStore(log)
	for range n {
		if _, err := store.Publish(api.Event{StreamID: stream, Scope: api.ScopeSession, Type: "tool_call"}); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}
	return store
}

// TestPerStreamOverflowDropsSubscription: when a stream's bounded buffer fills
// because its writer cannot drain (here, push delivery is gated), the daemon
// drops just that stream via event.subscription_closed and keeps the connection.
func TestPerStreamOverflowDropsSubscription(t *testing.T) {
	stream := api.StreamID("session:a")
	store := storeWithEvents(t, stream, 20)
	conn := idleConn(t)

	gate := make(chan struct{}) // never released until cleanup
	t.Cleanup(func() { close(gate) })
	var closed []api.SubscriptionClosedParams
	var mu sync.Mutex

	p := NewPusher(store)
	p.bufSize = 1
	p.deliver = func(_ *rpc.Conn, method string, params any) error {
		switch method {
		case MethodPush:
			<-gate // stall delivery so the buffer fills
			return nil
		case MethodSubscriptionClosed:
			mu.Lock()
			closed = append(closed, params.(api.SubscriptionClosedParams))
			mu.Unlock()
		}
		return nil
	}
	var connClosed bool
	p.closeConn = func(*rpc.Conn) { connClosed = true }

	if _, err := p.start(conn, api.EventsSubscribeParams{StreamID: stream, LastSeq: 0}); err != nil {
		t.Fatalf("start: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(closed)
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("expected a subscription_closed for the overflowing stream")
		}
		time.Sleep(time.Millisecond)
	}
	if closed[0].StreamID != stream || closed[0].Reason == "" {
		t.Errorf("subscription_closed = %+v", closed[0])
	}
	if connClosed {
		t.Error("per-stream overflow must keep the connection open")
	}
	// The dropped stream leaves no subscriber registered.
	p.mu.Lock()
	_, stillSubscribed := p.subs[stream]
	p.mu.Unlock()
	if stillSubscribed {
		t.Error("overflowing stream should be removed from subs")
	}
}

// TestConnCloseUnregistersSubscription: when the connection closes, the tailer
// exits and unregisters its store subscription, so a disconnect does not leak a
// dead subscriber that future publishes would keep signaling.
func TestConnCloseUnregistersSubscription(t *testing.T) {
	stream := api.StreamID("session:a")
	store := storeWithEvents(t, stream, 0) // empty: the tailer parks waiting for appends
	a, b := net.Pipe()
	conn := rpc.NewConn(a, nil)
	t.Cleanup(func() { _ = b.Close() })

	p := NewPusher(store)
	if _, err := p.start(conn, api.EventsSubscribeParams{StreamID: stream, LastSeq: 0}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if store.subscriberCount(stream) != 1 {
		t.Fatalf("subscriberCount = %d after subscribe, want 1", store.subscriberCount(stream))
	}

	_ = conn.Close()
	deadline := time.Now().Add(2 * time.Second)
	for store.subscriberCount(stream) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("subscriberCount = %d after disconnect, want 0 (leaked subscription)", store.subscriberCount(stream))
		}
		time.Sleep(time.Millisecond)
	}
}

// TestSocketStallDisconnects: a push write that exceeds the deadline means the
// client is not draining the socket, so the whole connection is disconnected.
func TestSocketStallDisconnects(t *testing.T) {
	stream := api.StreamID("session:a")
	store := storeWithEvents(t, stream, 5)
	conn := idleConn(t)

	disconnected := make(chan struct{}, 1)
	p := NewPusher(store)
	p.deliver = func(*rpc.Conn, string, any) error {
		return context.DeadlineExceeded // socket write timed out
	}
	p.closeConn = func(*rpc.Conn) {
		select {
		case disconnected <- struct{}{}:
		default:
		}
	}

	if _, err := p.start(conn, api.EventsSubscribeParams{StreamID: stream, LastSeq: 0}); err != nil {
		t.Fatalf("start: %v", err)
	}
	select {
	case <-disconnected:
	case <-time.After(2 * time.Second):
		t.Fatal("a stalled socket write should disconnect the whole client")
	}
}
