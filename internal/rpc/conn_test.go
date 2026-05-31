package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

// pair connects two Conns over an in-memory pipe with the given handlers and
// returns them; both are closed via t.Cleanup.
func pair(t *testing.T, ha, hb Handler) (*Conn, *Conn) {
	t.Helper()
	p1, p2 := net.Pipe()
	a := NewConn(p1, ha)
	b := NewConn(p2, hb)
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	return a, b
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}

type echoParams struct {
	Msg string `json:"msg"`
}

func TestCallResponse(t *testing.T) {
	// b echoes; a calls b.
	_, _ = pairEcho(t)
}

func pairEcho(t *testing.T) (*Conn, *Conn) {
	echo := HandlerFunc(func(_ context.Context, req *Request) (any, error) {
		var p echoParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		return echoParams{Msg: "re:" + p.Msg}, nil
	})
	a, b := pair(t, nil, echo)

	var got echoParams
	if err := a.Call(testCtx(t), "echo", echoParams{Msg: "hi"}, &got); err != nil {
		t.Fatalf("call: %v", err)
	}
	if got.Msg != "re:hi" {
		t.Fatalf("got %q, want %q", got.Msg, "re:hi")
	}
	return a, b
}

func TestBidirectional(t *testing.T) {
	// Both ends serve "inc" and call each other.
	inc := HandlerFunc(func(_ context.Context, req *Request) (any, error) {
		var n int
		if err := json.Unmarshal(req.Params, &n); err != nil {
			return nil, err
		}
		return n + 1, nil
	})
	a, b := pair(t, inc, inc)

	var ra, rb int
	if err := a.Call(testCtx(t), "inc", 1, &ra); err != nil {
		t.Fatalf("a->b: %v", err)
	}
	if err := b.Call(testCtx(t), "inc", 41, &rb); err != nil {
		t.Fatalf("b->a: %v", err)
	}
	if ra != 2 || rb != 42 {
		t.Fatalf("ra=%d rb=%d, want 2 and 42", ra, rb)
	}
}

func TestNotify(t *testing.T) {
	got := make(chan string, 1)
	h := HandlerFunc(func(_ context.Context, req *Request) (any, error) {
		if !req.Notification {
			t.Errorf("expected notification")
		}
		var p echoParams
		_ = json.Unmarshal(req.Params, &p)
		got <- p.Msg
		return nil, nil
	})
	a, _ := pair(t, nil, h)

	if err := a.Notify(testCtx(t), "note", echoParams{Msg: "ping"}); err != nil {
		t.Fatalf("notify: %v", err)
	}
	select {
	case m := <-got:
		if m != "ping" {
			t.Fatalf("got %q", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("notification not delivered")
	}
}

func TestConcurrentInFlight(t *testing.T) {
	inc := HandlerFunc(func(_ context.Context, req *Request) (any, error) {
		var n int
		if err := json.Unmarshal(req.Params, &n); err != nil {
			return nil, err
		}
		return n + 1, nil
	})
	a, b := pair(t, inc, inc)

	const n = 100
	var wg sync.WaitGroup
	errCh := make(chan error, 2*n)
	ctx := testCtx(t)

	call := func(c *Conn, v int) {
		defer wg.Done()
		var got int
		if err := c.Call(ctx, "inc", v, &got); err != nil {
			errCh <- err
			return
		}
		if got != v+1 {
			errCh <- errors.New("wrong correlation: bad result")
		}
	}
	for i := range n {
		wg.Add(2)
		go call(a, i)      // a -> b
		go call(b, 1000+i) // b -> a, concurrently
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent call: %v", err)
	}
}

func TestHandlerError(t *testing.T) {
	h := HandlerFunc(func(_ context.Context, _ *Request) (any, error) {
		return nil, &Error{Code: 1234, Message: "boom"}
	})
	a, _ := pair(t, nil, h)

	err := a.Call(testCtx(t), "fail", nil, nil)
	var rpcErr *Error
	if !errors.As(err, &rpcErr) {
		t.Fatalf("want *Error, got %v", err)
	}
	if rpcErr.Code != 1234 || rpcErr.Message != "boom" {
		t.Fatalf("got %+v", rpcErr)
	}
}

func TestMethodNotFound(t *testing.T) {
	a, _ := pair(t, nil, nil) // b has no handler
	err := a.Call(testCtx(t), "nope", nil, nil)
	var rpcErr *Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != codeMethodNotFound {
		t.Fatalf("want method-not-found, got %v", err)
	}
}

func TestCallAfterClose(t *testing.T) {
	a, _ := pair(t, nil, nil)
	_ = a.Close()
	if err := a.Call(testCtx(t), "x", nil, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("want ErrClosed, got %v", err)
	}
}
