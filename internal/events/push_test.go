package events

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/dispatch"
	"github.com/dusto/tend/internal/rpc"
)

// recorder is a client-side handler that captures event.push notifications.
type recorder struct{ ch chan api.Event }

func (r *recorder) Handle(_ context.Context, req *rpc.Request) (any, error) {
	if req.Method == MethodPush {
		var p api.EventPushParams
		if err := json.Unmarshal(req.Params, &p); err == nil {
			r.ch <- p.Event
		}
	}
	return nil, nil
}

func newPushPair(t *testing.T) (*Bus, *Log, *rpc.Conn, *recorder) {
	t.Helper()
	bus := NewBus()
	log, err := OpenLog(filepath.Join(t.TempDir(), "events.log"))
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })

	mux := dispatch.NewMux(api.PluginToDaemon)
	if err := RegisterClient(mux, NewPusher(bus, log)); err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}
	p1, p2 := net.Pipe()
	server := rpc.NewConn(p1, mux)
	rec := &recorder{ch: make(chan api.Event, 64)}
	client := rpc.NewConn(p2, rec)
	t.Cleanup(func() { _ = server.Close(); _ = client.Close() })
	return bus, log, client, rec
}

func subscribe(t *testing.T, c *rpc.Conn, streamID api.StreamID, lastSeq uint64) api.EventsSubscribeResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var res api.EventsSubscribeResult
	if err := c.Call(ctx, MethodSubscribe, api.EventsSubscribeParams{StreamID: streamID, LastSeq: lastSeq}, &res); err != nil {
		t.Fatalf("subscribe %s: %v", streamID, err)
	}
	return res
}

func recvEvent(t *testing.T, rec *recorder) api.Event {
	t.Helper()
	select {
	case ev := <-rec.ch:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event.push")
		return api.Event{}
	}
}

func recvSeqs(t *testing.T, rec *recorder, n int) []uint64 {
	t.Helper()
	out := make([]uint64, n)
	for i := range n {
		out[i] = recvEvent(t, rec).Seq
	}
	return out
}

func assertNoEvent(t *testing.T, rec *recorder) {
	t.Helper()
	select {
	case ev := <-rec.ch:
		t.Fatalf("unexpected event.push %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSubscribeForwardsLive(t *testing.T) {
	bus, _, client, rec := newPushPair(t)
	subscribe(t, client, "session:a", 0)

	for range 3 {
		bus.Publish(api.Event{StreamID: "session:a", Scope: api.ScopeSession, Type: "tool_call"})
	}
	if got := recvSeqs(t, rec, 3); !equal(got, []uint64{1, 2, 3}) {
		t.Fatalf("live seqs = %v, want [1 2 3]", got)
	}
}

func TestSubscribeReplaysHistory(t *testing.T) {
	bus, log, client, rec := newPushPair(t)
	_ = bus
	for i := range uint64(3) {
		if err := log.Append(api.Event{StreamID: "session:a", Scope: api.ScopeSession, Seq: i + 1}); err != nil {
			t.Fatal(err)
		}
	}

	res := subscribe(t, client, "session:a", 1)
	if res.Tail != 3 {
		t.Errorf("Tail = %d, want 3", res.Tail)
	}
	if got := recvSeqs(t, rec, 2); !equal(got, []uint64{2, 3}) {
		t.Fatalf("replay seqs = %v, want [2 3]", got)
	}
}

func TestReplayThenLive(t *testing.T) {
	bus, log, client, rec := newPushPair(t)
	// Align the bus sequence with the log (as the integrated daemon would): the
	// log holds 1..3 and the bus counter is advanced to 3 with no subscriber.
	for i := range uint64(3) {
		if err := log.Append(api.Event{StreamID: "session:a", Seq: i + 1}); err != nil {
			t.Fatal(err)
		}
		bus.Publish(api.Event{StreamID: "session:a"})
	}

	subscribe(t, client, "session:a", 0)
	// Replay 1,2,3 arrives first.
	if got := recvSeqs(t, rec, 3); !equal(got, []uint64{1, 2, 3}) {
		t.Fatalf("replay seqs = %v, want [1 2 3]", got)
	}
	// Live events continue the sequence and arrive after replay.
	bus.Publish(api.Event{StreamID: "session:a"})
	bus.Publish(api.Event{StreamID: "session:a"})
	if got := recvSeqs(t, rec, 2); !equal(got, []uint64{4, 5}) {
		t.Fatalf("live seqs = %v, want [4 5]", got)
	}
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	bus, _, client, rec := newPushPair(t)
	subscribe(t, client, "session:a", 0)

	bus.Publish(api.Event{StreamID: "session:a"})
	if got := recvEvent(t, rec); got.Seq != 1 {
		t.Fatalf("first Seq = %d, want 1", got.Seq)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Call(ctx, MethodUnsubscribe, api.EventsUnsubscribeParams{StreamID: "session:a"}, nil); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}

	// Give the pump time to observe the stop before publishing again.
	time.Sleep(50 * time.Millisecond)
	bus.Publish(api.Event{StreamID: "session:a"})
	assertNoEvent(t, rec)
}

func TestPushCarriesFields(t *testing.T) {
	bus, _, client, rec := newPushPair(t)
	subscribe(t, client, "session:a", 0)
	bus.Publish(api.Event{StreamID: "session:a", Scope: api.ScopeSession, Type: "tool_call"})

	ev := recvEvent(t, rec)
	if ev.StreamID != "session:a" || ev.Kind != api.KindEvent || ev.Seq != 1 || ev.CursorSeq != 1 {
		t.Fatalf("push event = %+v", ev)
	}
}

func TestSubscribeInsideCompactedRangeReturnsCursorCompacted(t *testing.T) {
	bus, log, client, rec := newPushPair(t)
	_ = bus
	for i := range uint64(10) {
		if err := log.Append(api.Event{StreamID: "session:a", Seq: i + 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.AppendSummary("session:a", api.ScopeSession, 3, 7, nil); err != nil {
		t.Fatal(err)
	}

	// last_seq=4 is inside the summarized range [3,7]: expect cursor_compacted
	// with from_seq=3 as the boundary, and no events delivered.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var res api.EventsSubscribeResult
	err := client.Call(ctx, MethodSubscribe, api.EventsSubscribeParams{StreamID: "session:a", LastSeq: 4}, &res)
	var rerr *rpc.Error
	if !errors.As(err, &rerr) || rerr.Code != api.ErrCursorCompacted {
		t.Fatalf("err = %v, want cursor_compacted (%d)", err, api.ErrCursorCompacted)
	}
	var data api.CursorCompactedData
	if jerr := json.Unmarshal(rerr.Data, &data); jerr != nil {
		t.Fatalf("decode data: %v", jerr)
	}
	if data.BoundarySeq != 3 || data.StreamID != "session:a" {
		t.Errorf("data = %+v, want boundary 3 on session:a", data)
	}
	assertNoEvent(t, rec)

	// A cursor before the range replays normally (summary served at 3, then 8..10).
	subscribe(t, client, "session:a", 2)
	if got := recvSeqs(t, rec, 4); !equal(got, []uint64{3, 8, 9, 10}) {
		t.Fatalf("replay seqs = %v, want [3 8 9 10]", got)
	}
}

// TestFailedSubscribeReleasesBusSubscription guards against the leak where a
// compacted/failed subscribe left an un-drained bus subscriber registered:
// later publishes would fill its buffer and block. After such a subscribe,
// publishing well past subBuffer must complete promptly.
func TestFailedSubscribeReleasesBusSubscription(t *testing.T) {
	bus, log, client, _ := newPushPair(t)
	for i := range uint64(5) {
		if err := log.Append(api.Event{StreamID: "session:a", Seq: i + 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.AppendSummary("session:a", api.ScopeSession, 2, 4, nil); err != nil {
		t.Fatal(err)
	}

	// Subscribe inside the compacted range -> cursor_compacted -> dropped.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var res api.EventsSubscribeResult
	if err := client.Call(ctx, MethodSubscribe, api.EventsSubscribeParams{StreamID: "session:a", LastSeq: 3}, &res); err == nil {
		t.Fatal("expected cursor_compacted error")
	}

	done := make(chan struct{})
	go func() {
		for range subBuffer + 5 {
			bus.Publish(api.Event{StreamID: "session:a"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publishing blocked: failed subscribe leaked a bus subscriber")
	}
}

func TestDoubleSubscribeRejected(t *testing.T) {
	_, _, client, _ := newPushPair(t)
	subscribe(t, client, "session:a", 0)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var res api.EventsSubscribeResult
	err := client.Call(ctx, MethodSubscribe, api.EventsSubscribeParams{StreamID: "session:a"}, &res)
	if err == nil {
		t.Fatal("second subscribe to same stream should fail")
	}
}
