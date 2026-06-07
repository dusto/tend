package events

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/dispatch"
	"github.com/dusto/tend/internal/rpc"
)

// Method names for the event subscription wire methods, matching the api
// contract.
const (
	MethodSubscribe          = "events.subscribe"
	MethodUnsubscribe        = "events.unsubscribe"
	MethodPush               = "event.push"
	MethodSubscriptionClosed = "event.subscription_closed"
)

// batch bounds how many records one tailer read returns, so catch-up over a
// busy stream proceeds in bounded steps rather than materializing the whole
// backlog at once.
const batch = 64

// Defaults for the per-connection backpressure tiers.
const (
	// defaultBufSize bounds a single stream's in-flight send buffer. When it
	// fills while the connection is otherwise healthy, just that stream's
	// subscription is dropped (per-stream overflow).
	defaultBufSize = 256
	// defaultWriteTimeout bounds one event.push write. Exceeding it means the
	// client has stopped draining the socket (socket-level overflow), so the
	// whole connection is disconnected.
	defaultWriteTimeout = 5 * time.Second
)

// Pusher serves the event subscription methods for one connection. On subscribe
// it starts, per stream, a tailer that reads the log forward from the client's
// cursor and follows live appends, and a writer that delivers buffered records
// as event.push notifications. Backpressure is two-tier: a full per-stream
// buffer drops just that stream (event.subscription_closed, connection kept); a
// stalled socket write disconnects the whole client. It is bound to the
// connection that served the subscribe request and is safe for concurrent use.
type Pusher struct {
	store        *Store
	bufSize      int
	writeTimeout time.Duration
	// deliver sends a daemon->client notification; the field is the seam tests
	// replace to drive the backpressure tiers deterministically.
	deliver func(conn *rpc.Conn, method string, params any) error
	// closeConn disconnects the client (socket-level overflow); a seam for tests.
	closeConn func(conn *rpc.Conn)

	mu   sync.Mutex
	subs map[api.StreamID]*tailer
}

// tailer is one stream's subscription: its log reader, its bounded send buffer,
// and the connection it pushes to.
type tailer struct {
	sub      *Sub
	conn     *rpc.Conn
	buf      chan api.Event
	lastSent atomic.Uint64 // highest cursor_seq written; diagnostic only
}

// NewPusher returns a Pusher backed by store with the default backpressure
// bounds.
func NewPusher(store *Store) *Pusher {
	p := &Pusher{
		store:        store,
		bufSize:      defaultBufSize,
		writeTimeout: defaultWriteTimeout,
		subs:         make(map[api.StreamID]*tailer),
	}
	p.deliver = p.notify
	p.closeConn = func(conn *rpc.Conn) { _ = conn.Close() }
	return p
}

// notify is the production delivery: a write-deadline-bounded notification.
func (p *Pusher) notify(conn *rpc.Conn, method string, params any) error {
	ctx, cancel := context.WithTimeout(context.Background(), p.writeTimeout)
	defer cancel()
	return conn.Notify(ctx, method, params)
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
	return p.start(conn, params)
}

// start registers and launches a subscription's tailer and writer. It is split
// from subscribe so tests can drive it with a connection directly.
func (p *Pusher) start(conn *rpc.Conn, params api.EventsSubscribeParams) (api.EventsSubscribeResult, error) {
	p.mu.Lock()
	if _, dup := p.subs[params.StreamID]; dup {
		p.mu.Unlock()
		return api.EventsSubscribeResult{}, &rpc.Error{Code: rpc.CodeInvalidRequest, Message: "events: already subscribed to " + string(params.StreamID)}
	}
	// Register for live wakeups before reading, so appends during the first read
	// are not missed: the tailer re-reads to the tail after each wake.
	t := &tailer{sub: p.store.Subscribe(params.StreamID), conn: conn, buf: make(chan api.Event, p.bufSize)}
	p.subs[params.StreamID] = t
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
	go p.tail(t, params.LastSeq)
	go p.write(t)
	return api.EventsSubscribeResult{Tail: tail}, nil
}

func (p *Pusher) unsubscribe(_ context.Context, params api.EventsUnsubscribeParams) (struct{}, error) {
	p.drop(params.StreamID)
	return struct{}{}, nil
}

// drop stops the tailer/writer for streamID (if any) and removes it.
func (p *Pusher) drop(streamID api.StreamID) {
	p.mu.Lock()
	t := p.subs[streamID]
	delete(p.subs, streamID)
	p.mu.Unlock()
	if t != nil {
		t.sub.Close()
	}
}

// tail reads the log forward from cursor in bounded batches and enqueues records
// into the stream's send buffer, in order. Replay and live are the same path, so
// there are no drops or reordering within the stream. A full buffer is
// per-stream overflow. On any exit — overflow, unsubscribe, or the connection
// closing — it drops the subscription, so the connection's termination
// unregisters the store subscription rather than leaking a dead subscriber.
func (p *Pusher) tail(t *tailer, cursor uint64) {
	defer p.drop(t.sub.streamID)
	for {
		records, _, err := p.store.Read(t.sub.streamID, cursor, batch)
		if err != nil {
			return
		}
		if len(records) > 0 {
			for _, ev := range records {
				select {
				case <-t.sub.Stop():
					return
				case <-t.conn.Done():
					return
				case t.buf <- ev:
					cursor = ev.CursorSeq
				default:
					p.overflow(t)
					return
				}
			}
			continue // keep reading until the backlog is drained
		}
		// Caught up to the tail: wait for the next append (or stop).
		select {
		case <-t.sub.Stop():
			return
		case <-t.conn.Done():
			return
		case <-t.sub.Wake():
		}
	}
}

// write drains the stream's buffer to the connection. A write that exceeds the
// deadline means the client is not draining the socket (socket-level overflow):
// the whole connection is disconnected.
func (p *Pusher) write(t *tailer) {
	for {
		select {
		case <-t.sub.Stop():
			return
		case <-t.conn.Done():
			return
		case ev := <-t.buf:
			if err := p.deliver(t.conn, MethodPush, api.EventPushParams{Event: ev}); err != nil {
				if errors.Is(err, context.DeadlineExceeded) {
					// Socket-level overflow: the client is not draining; disconnect
					// the whole client (the tailer then drops on conn close).
					p.closeConn(t.conn)
				} else {
					// Any other delivery failure stops this stream's writer, so drop
					// the subscription rather than leave the tailer with no writer.
					p.drop(t.sub.streamID)
				}
				return
			}
			t.lastSent.Store(ev.CursorSeq)
		}
	}
}

// overflow handles a full per-stream buffer: it drops just that stream (keeping
// the connection) and tells the client, which resubscribes from its own
// last_seq. If that notification itself stalls, the socket is not draining at
// all, so the whole connection is disconnected.
func (p *Pusher) overflow(t *tailer) {
	p.drop(t.sub.streamID)
	last := t.lastSent.Load()
	params := api.SubscriptionClosedParams{
		StreamID:    t.sub.streamID,
		Reason:      "per-stream buffer overflow",
		LastSentSeq: &last,
	}
	if err := p.deliver(t.conn, MethodSubscriptionClosed, params); errors.Is(err, context.DeadlineExceeded) {
		p.closeConn(t.conn)
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
