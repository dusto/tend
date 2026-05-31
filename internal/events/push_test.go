package events

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"sync"
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

func newPushPair(t *testing.T) (*Store, *rpc.Conn, *recorder) {
	t.Helper()
	log, err := OpenLog(filepath.Join(t.TempDir(), "events.log"))
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	store := NewStore(log)

	mux := dispatch.NewMux(api.PluginToDaemon)
	if err := RegisterClient(mux, NewPusher(store)); err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}
	p1, p2 := net.Pipe()
	server := rpc.NewConn(p1, mux)
	rec := &recorder{ch: make(chan api.Event, 1024)}
	client := rpc.NewConn(p2, rec)
	t.Cleanup(func() { _ = server.Close(); _ = client.Close() })
	return store, client, rec
}

func publishN(t *testing.T, store *Store, streamID api.StreamID, n int) {
	t.Helper()
	for range n {
		if _, err := store.Publish(api.Event{StreamID: streamID, Scope: api.ScopeSession, Type: "tool_call"}); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}
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
	store, client, rec := newPushPair(t)
	subscribe(t, client, "session:a", 0)

	publishN(t, store, "session:a", 3)
	if got := recvSeqs(t, rec, 3); !equal(got, []uint64{1, 2, 3}) {
		t.Fatalf("live seqs = %v, want [1 2 3]", got)
	}
}

func TestSubscribeReplaysHistory(t *testing.T) {
	store, client, rec := newPushPair(t)
	publishN(t, store, "session:a", 3)

	res := subscribe(t, client, "session:a", 1)
	if res.Tail != 3 {
		t.Errorf("Tail = %d, want 3", res.Tail)
	}
	if got := recvSeqs(t, rec, 2); !equal(got, []uint64{2, 3}) {
		t.Fatalf("replay seqs = %v, want [2 3]", got)
	}
}

func TestReplayStrictlyBeforeLive(t *testing.T) {
	store, client, rec := newPushPair(t)
	publishN(t, store, "session:a", 3) // history

	subscribe(t, client, "session:a", 0)
	publishN(t, store, "session:a", 2) // live, continues the same sequence

	// One source (the log), so replay and live form one contiguous 1..5.
	if got := recvSeqs(t, rec, 5); !equal(got, []uint64{1, 2, 3, 4, 5}) {
		t.Fatalf("seqs = %v, want [1 2 3 4 5]", got)
	}
}

// TestMidStreamReconnect models a client resuming from a stored cursor: it gets
// exactly the events after that cursor, in order, then live continues.
func TestMidStreamReconnect(t *testing.T) {
	store, client, rec := newPushPair(t)
	publishN(t, store, "session:a", 5)

	res := subscribe(t, client, "session:a", 3) // reconnect from last_seq=3
	if res.Tail != 5 {
		t.Errorf("Tail = %d, want 5", res.Tail)
	}
	if got := recvSeqs(t, rec, 2); !equal(got, []uint64{4, 5}) {
		t.Fatalf("resume seqs = %v, want [4 5]", got)
	}
	publishN(t, store, "session:a", 1)
	if got := recvEvent(t, rec); got.Seq != 6 {
		t.Fatalf("live after resume Seq = %d, want 6", got.Seq)
	}
}

// TestBusyStreamCatchUp drives constant production while the tailer catches up
// from the start, asserting it receives a contiguous 1..N with no drops,
// reordering, or livelock despite the bounded batch size.
func TestBusyStreamCatchUp(t *testing.T) {
	store, client, rec := newPushPair(t)
	const total = batch*8 + 5 // several batches' worth

	publishN(t, store, "session:a", batch+3) // some history before subscribe
	subscribe(t, client, "session:a", 0)

	var producer sync.WaitGroup
	producer.Go(func() {
		for i := batch + 3; i < total; i++ {
			if _, err := store.Publish(api.Event{StreamID: "session:a"}); err != nil {
				return
			}
		}
	})

	for want := uint64(1); want <= total; want++ {
		if got := recvEvent(t, rec).Seq; got != want {
			t.Fatalf("Seq = %d, want %d (drop or reorder)", got, want)
		}
	}
	producer.Wait()
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	store, client, rec := newPushPair(t)
	subscribe(t, client, "session:a", 0)

	publishN(t, store, "session:a", 1)
	if got := recvEvent(t, rec); got.Seq != 1 {
		t.Fatalf("first Seq = %d, want 1", got.Seq)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Call(ctx, MethodUnsubscribe, api.EventsUnsubscribeParams{StreamID: "session:a"}, nil); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	time.Sleep(50 * time.Millisecond) // let the tailer observe the stop
	publishN(t, store, "session:a", 1)
	assertNoEvent(t, rec)
}

func TestPushCarriesFields(t *testing.T) {
	store, client, rec := newPushPair(t)
	subscribe(t, client, "session:a", 0)
	publishN(t, store, "session:a", 1)

	ev := recvEvent(t, rec)
	if ev.StreamID != "session:a" || ev.Kind != api.KindEvent || ev.Seq != 1 || ev.CursorSeq != 1 {
		t.Fatalf("push event = %+v", ev)
	}
}

func TestSubscribeInsideCompactedRangeReturnsCursorCompacted(t *testing.T) {
	store, client, rec := newPushPair(t)
	publishN(t, store, "session:a", 10)
	if err := store.log.AppendSummary("session:a", api.ScopeSession, 3, 7, nil); err != nil {
		t.Fatal(err)
	}

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
	// A rejected subscribe leaves no lingering subscriber registered.
	if n := store.subscriberCount("session:a"); n != 0 {
		t.Errorf("subscriberCount = %d after rejected subscribe, want 0", n)
	}

	// A cursor before the range replays normally (summary served at 3, then 8..10).
	subscribe(t, client, "session:a", 2)
	if got := recvSeqs(t, rec, 4); !equal(got, []uint64{3, 8, 9, 10}) {
		t.Fatalf("replay seqs = %v, want [3 8 9 10]", got)
	}
}

func TestDoubleSubscribeRejected(t *testing.T) {
	_, client, _ := newPushPair(t)
	subscribe(t, client, "session:a", 0)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var res api.EventsSubscribeResult
	if err := client.Call(ctx, MethodSubscribe, api.EventsSubscribeParams{StreamID: "session:a"}, &res); err == nil {
		t.Fatal("second subscribe to same stream should fail")
	}
}
