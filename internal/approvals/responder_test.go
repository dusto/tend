package approvals

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/rpc"
)

type fakeResponder struct{ capable bool }

func (f fakeResponder) CanRespondToPrompts() bool { return f.capable }

func selfFn(capable, registered bool) SelfFunc {
	return func() (Responder, bool) {
		if !registered {
			return nil, false
		}
		return fakeResponder{capable: capable}, true
	}
}

// startPending begins a blocked approval and returns a channel for its outcome.
func startPending(t *testing.T, g *Gate, detail json.RawMessage) chan Outcome {
	t.Helper()
	out := make(chan Outcome, 1)
	go func() {
		o, _ := g.Request(context.Background(), runningSession(t), "file_edit", detail)
		out <- o
	}()
	waitFor(t, func() bool { _, ok := g.Get("appr-1"); return ok })
	return out
}

func fixedGate() *Gate {
	return NewGate(nil, Options{NewID: func() api.ApprovalID { return "appr-1" }})
}

func TestResponderRespondCapabilityGated(t *testing.T) {
	g := fixedGate()
	out := startPending(t, g, nil)

	// A client that is not prompt-capable cannot resolve it.
	notCapable := &responder{gate: g, self: selfFn(false, true)}
	_, err := notCapable.respond(context.Background(), api.ApprovalRespondParams{ApprovalID: "appr-1", Approved: true})
	var rpcErr *rpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != api.ErrNotPromptCapable {
		t.Fatalf("err = %v, want not_prompt_capable", err)
	}
	if _, ok := g.Get("appr-1"); !ok {
		t.Fatal("approval should still be pending after a gated refusal")
	}

	// An unregistered connection also cannot.
	if _, err := (&responder{gate: g, self: selfFn(true, false)}).respond(context.Background(), api.ApprovalRespondParams{ApprovalID: "appr-1"}); err == nil {
		t.Error("unregistered client should not be able to respond")
	}

	// A prompt-capable client resolves it.
	capable := &responder{gate: g, self: selfFn(true, true)}
	if _, err := capable.respond(context.Background(), api.ApprovalRespondParams{ApprovalID: "appr-1", Approved: true}); err != nil {
		t.Fatalf("respond: %v", err)
	}
	if o := <-out; !o.Approved {
		t.Errorf("outcome = %+v, want approved", o)
	}
}

func TestResponderRespondUnknown(t *testing.T) {
	g := fixedGate()
	h := &responder{gate: g, self: selfFn(true, true)}
	_, err := h.respond(context.Background(), api.ApprovalRespondParams{ApprovalID: "ghost", Approved: true})
	var rpcErr *rpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != rpc.CodeInvalidParams {
		t.Errorf("err = %v, want invalid params for unknown approval", err)
	}
}

func TestResponderList(t *testing.T) {
	g := fixedGate()
	detail := json.RawMessage(`{"kind":"file_edit"}`)
	out := startPending(t, g, detail)
	defer func() {
		_ = g.Resolve("appr-1", Decision{Approved: false})
		<-out
	}()

	h := &responder{gate: g, self: selfFn(true, true)}
	res, _ := h.list(context.Background(), api.ApprovalListParams{})
	if len(res.Approvals) != 1 {
		t.Fatalf("list len = %d, want 1", len(res.Approvals))
	}
	got := res.Approvals[0]
	if got.ApprovalID != "appr-1" || got.SessionID != "s1" || string(got.Detail) != string(detail) {
		t.Errorf("summary = %+v", got)
	}

	// Filtering by a different session yields nothing.
	res2, _ := h.list(context.Background(), api.ApprovalListParams{SessionID: "other"})
	if len(res2.Approvals) != 0 {
		t.Errorf("filtered list len = %d, want 0", len(res2.Approvals))
	}
}
