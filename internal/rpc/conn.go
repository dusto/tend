package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"sync"
	"time"
)

// ErrClosed is returned by Call/Notify once the connection is closed.
var ErrClosed = errors.New("rpc: connection closed")

// Handler serves inbound requests and notifications from the peer. For a
// notification (Request.Notification == true) the return values are ignored.
// Returning an *Error sends that JSON-RPC error verbatim; any other error is
// reported to the peer as an internal error.
type Handler interface {
	Handle(ctx context.Context, req *Request) (any, error)
}

// Caller is the request side of a peer: it sends a request and waits for the
// reply. *Conn satisfies it. It lets code that only needs to issue calls (such
// as routing a reverse-direction call to a bound editor) depend on the
// capability rather than the whole Conn.
type Caller interface {
	Call(ctx context.Context, method string, params, result any) error
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(ctx context.Context, req *Request) (any, error)

// Handle calls f.
func (f HandlerFunc) Handle(ctx context.Context, req *Request) (any, error) { return f(ctx, req) }

// Conn is a bidirectional JSON-RPC 2.0 peer over a single connection. Both ends
// may Call and Notify, and both serve inbound traffic via their Handler; there
// is no wire-level client/server role. A Conn is safe for concurrent use.
type Conn struct {
	rwc io.ReadWriteCloser
	h   Handler

	dec *json.Decoder

	writeSem chan struct{} // cap-1 semaphore guarding enc; allows ctx-aware acquisition
	enc      *json.Encoder

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu       sync.Mutex
	nextID   int64
	pending  map[int64]chan *message
	closed   bool
	closeErr error
}

// NewConn starts a JSON-RPC peer over rwc, dispatching inbound traffic to h
// (which may be nil to reject all inbound calls with method-not-found). The
// read loop runs until rwc errors or Close is called.
func NewConn(rwc io.ReadWriteCloser, h Handler) *Conn {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Conn{
		rwc:      rwc,
		h:        h,
		dec:      json.NewDecoder(rwc),
		enc:      json.NewEncoder(rwc),
		writeSem: make(chan struct{}, 1),
		ctx:      ctx,
		cancel:   cancel,
		done:     make(chan struct{}),
		pending:  make(map[int64]chan *message),
	}
	go c.readLoop()
	return c
}

// Call sends a request and blocks until the response arrives, ctx is done, or
// the connection closes. params and result may be nil. A JSON-RPC error from
// the peer is returned as *Error.
func (c *Conn) Call(ctx context.Context, method string, params, result any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := toRaw(params)
	if err != nil {
		return err
	}
	id, ch, err := c.register()
	if err != nil {
		return err
	}
	defer c.unregister(id)

	if err := c.send(ctx, &message{JSONRPC: version, ID: idJSON(id), Method: method, Params: raw}); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.err()
	case m := <-ch:
		if m.Error != nil {
			return m.Error
		}
		if result != nil && len(m.Result) > 0 {
			return json.Unmarshal(m.Result, result)
		}
		return nil
	}
}

// Notify sends a notification (no response expected).
func (c *Conn) Notify(ctx context.Context, method string, params any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := toRaw(params)
	if err != nil {
		return err
	}
	return c.send(ctx, &message{JSONRPC: version, Method: method, Params: raw})
}

// Close shuts down the connection; pending and future calls return ErrClosed.
func (c *Conn) Close() error {
	c.closeWith(ErrClosed)
	return nil
}

// Done is closed when the connection is no longer usable.
func (c *Conn) Done() <-chan struct{} { return c.done }

func (c *Conn) register() (int64, chan *message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, nil, c.closeErr
	}
	c.nextID++
	id := c.nextID
	ch := make(chan *message, 1)
	c.pending[id] = ch
	return id, ch, nil
}

func (c *Conn) unregister(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *Conn) send(ctx context.Context, m *message) error {
	// Already-cancelled context must never put bytes on the wire.
	if err := ctx.Err(); err != nil {
		return err
	}
	// Acquire the write token, respecting ctx and connection close.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.err()
	case c.writeSem <- struct{}{}:
	}
	defer func() { <-c.writeSem }()

	// Honor a ctx deadline during the write when the conn supports it.
	if dl, ok := ctx.Deadline(); ok {
		if dc, ok := c.rwc.(interface{ SetWriteDeadline(time.Time) error }); ok {
			_ = dc.SetWriteDeadline(dl)
			defer func() { _ = dc.SetWriteDeadline(time.Time{}) }()
		}
	}
	return c.enc.Encode(m)
}

func (c *Conn) err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeErr
}

func (c *Conn) readLoop() {
	for {
		var m message
		if err := c.dec.Decode(&m); err != nil {
			c.closeWith(err)
			return
		}
		c.dispatch(&m)
	}
}

func (c *Conn) dispatch(m *message) {
	switch {
	case m.isResponse():
		id, err := parseID(m.ID)
		if err != nil {
			return
		}
		c.mu.Lock()
		ch := c.pending[id]
		c.mu.Unlock()
		if ch != nil {
			ch <- m // buffered (cap 1); the id is unique so this never blocks
		}
	case m.isRequest():
		// Requests may run concurrently; one slow handler must not block others.
		go c.serve(m, false)
	case m.isNotification():
		// Notifications are served inline on the read loop so they are handled in
		// receipt (wire) order — streamed notifications (e.g. event.push) rely on
		// this. A notification handler must therefore not block on a Call over the
		// same connection.
		c.serve(m, true)
	}
}

func (c *Conn) serve(m *message, notification bool) {
	req := &Request{Method: m.Method, Params: m.Params, Notification: notification}
	result, err := c.handle(req)
	if notification {
		return
	}
	resp := &message{JSONRPC: version, ID: m.ID}
	if err != nil {
		resp.Error = toError(err)
	} else if raw, mErr := toRaw(result); mErr != nil {
		resp.Error = &Error{Code: CodeInternalError, Message: "internal error: " + mErr.Error()}
	} else {
		resp.Result = raw
	}
	_ = c.send(c.ctx, resp)
}

func (c *Conn) handle(req *Request) (any, error) {
	if c.h == nil {
		return nil, &Error{Code: CodeMethodNotFound, Message: "method not found: " + req.Method}
	}
	return c.h.Handle(contextWithConn(c.ctx, c), req)
}

type connCtxKey struct{}

func contextWithConn(ctx context.Context, c *Conn) context.Context {
	return context.WithValue(ctx, connCtxKey{}, c)
}

// ConnFromContext returns the Conn serving the current inbound request, so a
// handler can send reverse-direction calls or notifications back to the peer.
// It returns nil if ctx does not carry a Conn.
func ConnFromContext(ctx context.Context) *Conn {
	c, _ := ctx.Value(connCtxKey{}).(*Conn)
	return c
}

func (c *Conn) closeWith(err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	if err == nil || errors.Is(err, io.EOF) {
		err = ErrClosed
	}
	c.closeErr = err
	c.mu.Unlock()

	c.cancel()
	close(c.done) // unblocks any pending Call via its select on c.done
	_ = c.rwc.Close()
}

func toError(err error) *Error {
	if e, ok := errors.AsType[*Error](err); ok {
		return e
	}
	return &Error{Code: CodeInternalError, Message: err.Error()}
}

func idJSON(id int64) json.RawMessage {
	return json.RawMessage(strconv.FormatInt(id, 10))
}

func parseID(raw json.RawMessage) (int64, error) {
	var id int64
	if err := json.Unmarshal(raw, &id); err != nil {
		return 0, err
	}
	return id, nil
}
