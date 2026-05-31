package events

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/dispatch"
	"github.com/dusto/tend/internal/rpc"
)

// Method names for the event subscription wire methods, matching the api
// contract.
const (
	MethodSubscribe   = "events.subscribe"
	MethodUnsubscribe = "events.unsubscribe"
	MethodPush        = "event.push"
)

// batch bounds how many records one tailer read returns, so catch-up over a
// busy stream proceeds in bounded steps rather than materializing the whole
// backlog at once.
const batch = 64

// Pusher serves the event subscription methods for one connection. On subscribe
// it starts a single tailer per stream that replays the log forward from the
// client's cursor and then follows live appends, delivering each record as an
// event.push notification. It is bound to the connection that served the
// subscribe request and is safe for concurrent use.
type Pusher struct {
	store *Store

	mu   sync.Mutex
	subs map[api.StreamID]*Sub
}

// NewPusher returns a Pusher backed by store.
func NewPusher(store *Store) *Pusher {
	return &Pusher{store: store, subs: make(map[api.StreamID]*Sub)}
}

// RegisterClient installs events.subscribe and events.unsubscribe on m, backed
// by p.
func RegisterClient(m *dispatch.Mux, p *Pusher) error {
	if err := dispatch.Handle(m, MethodSubscribe, p.subscribe); err != nil {
		return err
	}
	return dispatch.Handle(m, MethodUnsubscribe, p.unsubscribe)
}

func (p *Pusher) subscribe(ctx context.Context, params api.EventsSubscribeParams) (api.EventsSubscribeResult, error) {
	conn := rpc.ConnFromContext(ctx)
	if conn == nil {
		return api.EventsSubscribeResult{}, &rpc.Error{Code: rpc.CodeInternalError, Message: "events: no connection in context"}
	}

	p.mu.Lock()
	if _, dup := p.subs[params.StreamID]; dup {
		p.mu.Unlock()
		return api.EventsSubscribeResult{}, &rpc.Error{Code: rpc.CodeInvalidRequest, Message: "events: already subscribed to " + string(params.StreamID)}
	}
	// Register for live wakeups before reading, so appends during the first read
	// are not missed: the tailer re-reads to the tail after each wake.
	sub := p.store.Subscribe(params.StreamID)
	p.subs[params.StreamID] = sub
	p.mu.Unlock()

	// Reject a cursor that falls inside a compacted range before delivering
	// anything; the client resumes from the summary boundary.
	if _, compactedFrom, err := p.store.Read(params.StreamID, params.LastSeq, 1); err != nil {
		p.drop(params.StreamID)
		return api.EventsSubscribeResult{}, &rpc.Error{Code: rpc.CodeInternalError, Message: err.Error()}
	} else if compactedFrom != 0 {
		p.drop(params.StreamID)
		return api.EventsSubscribeResult{}, compactedError(params.StreamID, compactedFrom)
	}

	tail := p.store.HighWater(params.StreamID)
	go p.tail(conn, sub, params.LastSeq)
	return api.EventsSubscribeResult{Tail: tail}, nil
}

func (p *Pusher) unsubscribe(_ context.Context, params api.EventsUnsubscribeParams) (struct{}, error) {
	p.drop(params.StreamID)
	return struct{}{}, nil
}

// drop stops the tailer for streamID (if any) and removes it.
func (p *Pusher) drop(streamID api.StreamID) {
	p.mu.Lock()
	sub := p.subs[streamID]
	delete(p.subs, streamID)
	p.mu.Unlock()
	if sub != nil {
		sub.Close()
	}
}

// tail is the single per-stream reader: it reads the log forward from cursor in
// bounded batches until caught up, then waits for a wake and reads again. Replay
// and live delivery are the same path, so live events are delivered strictly
// after replayed history with no drops or reordering. It exits when the
// subscription is closed or the connection ends.
func (p *Pusher) tail(conn *rpc.Conn, sub *Sub, cursor uint64) {
	defer sub.Close()
	for {
		records, _, err := p.store.Read(sub.streamID, cursor, batch)
		if err != nil {
			return
		}
		if len(records) > 0 {
			for _, ev := range records {
				// Re-check cancellation before each record so unsubscribe (or a
				// closed connection) stops delivery within one record, not one
				// whole batch.
				select {
				case <-sub.Stop():
					return
				case <-conn.Done():
					return
				default:
				}
				if !push(conn, ev) {
					return
				}
				cursor = ev.CursorSeq
			}
			continue // keep reading until the backlog is drained
		}
		// Caught up to the tail: wait for the next append (or stop).
		select {
		case <-sub.Stop():
			return
		case <-conn.Done():
			return
		case <-sub.Wake():
		}
	}
}

// compactedError builds the cursor_compacted error carrying the resume boundary.
func compactedError(streamID api.StreamID, boundary uint64) *rpc.Error {
	data, _ := json.Marshal(api.CursorCompactedData{StreamID: streamID, BoundarySeq: boundary})
	return &rpc.Error{
		Code:    api.ErrCursorCompacted,
		Message: "events: cursor inside a compacted range; resume from the summary boundary",
		Data:    data,
	}
}

// push sends one event.push notification, reporting whether it was sent.
func push(conn *rpc.Conn, ev api.Event) bool {
	return conn.Notify(context.Background(), MethodPush, api.EventPushParams{Event: ev}) == nil
}
