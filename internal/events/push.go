package events

import (
	"context"
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

// Pusher serves the event subscription methods for one connection. On subscribe
// it replays a stream's history from the log, then forwards live bus events to
// the client as event.push notifications, one ordered pump goroutine per
// stream. It is bound to the connection that served the subscribe request and
// is safe for concurrent use.
type Pusher struct {
	bus *Bus
	log *Log

	mu   sync.Mutex
	subs map[api.StreamID]*pump
}

// pump is one stream's live forwarding goroutine state.
type pump struct {
	sub  *Subscription
	stop chan struct{}
}

// NewPusher returns a Pusher backed by bus (live events) and log (replay).
func NewPusher(bus *Bus, log *Log) *Pusher {
	return &Pusher{bus: bus, log: log, subs: make(map[api.StreamID]*pump)}
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
	// Subscribe to live events before reading the log so events published during
	// replay are buffered and delivered after it, never dropped.
	pm := &pump{sub: p.bus.Subscribe(params.StreamID), stop: make(chan struct{})}
	p.subs[params.StreamID] = pm
	p.mu.Unlock()

	tail := p.log.HighWater(params.StreamID)
	replay, _, err := p.log.Replay(params.StreamID, params.LastSeq)
	if err != nil {
		p.drop(params.StreamID)
		return api.EventsSubscribeResult{}, &rpc.Error{Code: rpc.CodeInternalError, Message: err.Error()}
	}

	go p.run(conn, pm, replay)
	return api.EventsSubscribeResult{Tail: tail}, nil
}

func (p *Pusher) unsubscribe(_ context.Context, params api.EventsUnsubscribeParams) (struct{}, error) {
	p.drop(params.StreamID)
	return struct{}{}, nil
}

// drop stops the pump for streamID (if any) and removes it.
func (p *Pusher) drop(streamID api.StreamID) {
	p.mu.Lock()
	pm := p.subs[streamID]
	delete(p.subs, streamID)
	p.mu.Unlock()
	if pm != nil {
		close(pm.stop)
	}
}

// run replays history then forwards live events for one stream, in order, until
// the subscription is dropped or the connection closes. Live events at or below
// the last delivered cursor (already covered by replay) are skipped.
func (p *Pusher) run(conn *rpc.Conn, pm *pump, replay []api.Event) {
	defer pm.sub.Close()

	var delivered uint64
	for _, ev := range replay {
		if !push(conn, ev) {
			return
		}
		delivered = ev.CursorSeq
	}
	for {
		select {
		case <-pm.stop:
			return
		case <-conn.Done():
			return
		case ev := <-pm.sub.C:
			if ev.Seq <= delivered {
				continue
			}
			if !push(conn, ev) {
				return
			}
			delivered = ev.CursorSeq
		}
	}
}

// push sends one event.push notification, reporting whether it was sent.
func push(conn *rpc.Conn, ev api.Event) bool {
	return conn.Notify(context.Background(), MethodPush, api.EventPushParams{Event: ev}) == nil
}
