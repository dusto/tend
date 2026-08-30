package acp_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/acp"
	"github.com/dusto/tend/internal/approvals"
	"github.com/dusto/tend/internal/rpc"
	"github.com/dusto/tend/internal/session"
)

// fakeApprover records the gate call and returns a canned outcome/error.
type fakeApprover struct {
	called  bool
	gotKind string
	gotSess *session.Session
	gotCtx  context.Context
	detail  json.RawMessage
	outcome approvals.Outcome
	err     error
}

func (f *fakeApprover) Request(ctx context.Context, sess *session.Session, kind string, detail json.RawMessage) (approvals.Outcome, error) {
	f.called = true
	f.gotKind, f.gotSess, f.gotCtx, f.detail = kind, sess, ctx, detail
	return f.outcome, f.err
}

// fakeLookup resolves one session id.
type fakeLookup struct {
	sess *session.Session
	ok   bool
}

func (f fakeLookup) Get(api.SessionID) (*session.Session, bool) { return f.sess, f.ok }

// recordingNext records delegated (non-permission) messages.
type recordingNext struct {
	called bool
	method string
}

func (r *recordingNext) Handle(_ context.Context, req *rpc.Request) (any, error) {
	r.called = true
	r.method = req.Method
	return "delegated", nil
}

func permReq(t *testing.T, params any) *rpc.Request {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return &rpc.Request{Method: acp.PermissionMethod, Params: raw}
}

// standardOptions mirrors what claude-agent-acp offers for a normal tool.
func standardOptions() []map[string]string {
	return []map[string]string{
		{"kind": "allow_always", "name": "Allow always", "optionId": "allow_always"},
		{"kind": "allow_once", "name": "Allow", "optionId": "allow"},
		{"kind": "reject_once", "name": "Reject", "optionId": "reject"},
	}
}

func permParams(sessionID string) map[string]any {
	return map[string]any{
		"sessionId": sessionID,
		"toolCall": map[string]any{
			"toolCallId": "tc-1",
			"title":      "Write file",
			"kind":       "edit",
			"rawInput":   map[string]any{"file_path": "/x.go", "content": "package x"},
		},
		"options": standardOptions(),
	}
}

func decode(t *testing.T, v any) permOutcome {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var got struct {
		Outcome permOutcome `json:"outcome"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return got.Outcome
}

type permOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId"`
}

func newRouter(gate acp.Approver, lookup acp.SessionLookup, next rpc.Handler) *acp.PermissionRouter {
	return acp.NewPermissionRouter(next, gate, lookup, nil)
}

func TestNotificationDelegatesToNext(t *testing.T) {
	next := &recordingNext{}
	r := newRouter(&fakeApprover{}, fakeLookup{}, next)
	req := &rpc.Request{Method: "session/update", Notification: true, Params: json.RawMessage(`{}`)}
	if _, err := r.Handle(context.Background(), req); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !next.called || next.method != "session/update" {
		t.Errorf("notification not delegated: %+v", next)
	}
}

func TestOtherRequestDelegatesToNext(t *testing.T) {
	next := &recordingNext{}
	r := newRouter(&fakeApprover{}, fakeLookup{}, next)
	req := &rpc.Request{Method: "fs/read_text_file", Params: json.RawMessage(`{}`)}
	if _, err := r.Handle(context.Background(), req); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !next.called {
		t.Error("non-permission request was not delegated to next")
	}
}

func TestApprovedSelectsAllowOnce(t *testing.T) {
	gate := &fakeApprover{outcome: approvals.Outcome{Approved: true}}
	lookup := fakeLookup{sess: &session.Session{ID: "s1", WorkspaceID: "ws1"}, ok: true}
	r := newRouter(gate, lookup, &recordingNext{})

	resp, err := r.Handle(context.Background(), permReq(t, permParams("s1")))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	out := decode(t, resp)
	if out.Outcome != "selected" || out.OptionID != "allow" {
		t.Errorf("approve outcome = %+v, want selected/allow", out)
	}
	if !gate.called || gate.gotKind != api.ApprovalAgentTool {
		t.Errorf("gate not called with agent_tool kind: %+v", gate)
	}
	// The detail carries the real tool title, kind, and input.
	var d api.ApprovalDetail
	if err := json.Unmarshal(gate.detail, &d); err != nil {
		t.Fatalf("detail: %v", err)
	}
	if d.AgentTool == nil || d.AgentTool.Title != "Write file" || d.AgentTool.ToolKind != "edit" {
		t.Fatalf("detail agent_tool = %+v", d.AgentTool)
	}
	if len(d.AgentTool.RawInput) == 0 || !json.Valid(d.AgentTool.RawInput) {
		t.Errorf("raw input not carried: %s", d.AgentTool.RawInput)
	}
}

func TestDeniedSelectsRejectOnce(t *testing.T) {
	gate := &fakeApprover{outcome: approvals.Outcome{Approved: false}}
	lookup := fakeLookup{sess: &session.Session{ID: "s1"}, ok: true}
	r := newRouter(gate, lookup, &recordingNext{})

	resp, err := r.Handle(context.Background(), permReq(t, permParams("s1")))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	out := decode(t, resp)
	if out.Outcome != "selected" || out.OptionID != "reject" {
		t.Errorf("deny outcome = %+v, want selected/reject", out)
	}
}

func TestUnknownSessionCancelsWithoutGating(t *testing.T) {
	gate := &fakeApprover{}
	r := newRouter(gate, fakeLookup{ok: false}, &recordingNext{})

	resp, err := r.Handle(context.Background(), permReq(t, permParams("missing")))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if decode(t, resp).Outcome != "cancelled" {
		t.Errorf("unknown session should cancel, got %+v", decode(t, resp))
	}
	if gate.called {
		t.Error("gate must not be called for an unknown session")
	}
}

func TestGateErrorCancels(t *testing.T) {
	gate := &fakeApprover{err: errors.New("session not running")}
	lookup := fakeLookup{sess: &session.Session{ID: "s1"}, ok: true}
	r := newRouter(gate, lookup, &recordingNext{})

	resp, err := r.Handle(context.Background(), permReq(t, permParams("s1")))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if decode(t, resp).Outcome != "cancelled" {
		t.Errorf("gate error should cancel, got %+v", decode(t, resp))
	}
}

func TestMalformedParamsCancels(t *testing.T) {
	gate := &fakeApprover{}
	r := newRouter(gate, fakeLookup{sess: &session.Session{}, ok: true}, &recordingNext{})
	req := &rpc.Request{Method: acp.PermissionMethod, Params: json.RawMessage(`{bad`)}

	resp, err := r.Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if decode(t, resp).Outcome != "cancelled" {
		t.Errorf("malformed params should cancel, got %+v", decode(t, resp))
	}
	if gate.called {
		t.Error("gate must not be called for malformed params")
	}
}

func TestApprovedButNoAllowOptionCancels(t *testing.T) {
	gate := &fakeApprover{outcome: approvals.Outcome{Approved: true}}
	lookup := fakeLookup{sess: &session.Session{ID: "s1"}, ok: true}
	r := newRouter(gate, lookup, &recordingNext{})

	params := permParams("s1")
	params["options"] = []map[string]string{{"kind": "reject_once", "optionId": "reject"}}
	resp, err := r.Handle(context.Background(), permReq(t, params))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if decode(t, resp).Outcome != "cancelled" {
		t.Errorf("approve with no allow option should cancel, got %+v", decode(t, resp))
	}
}

func TestDeniedWithNoRejectOptionCancels(t *testing.T) {
	gate := &fakeApprover{outcome: approvals.Outcome{Approved: false}}
	lookup := fakeLookup{sess: &session.Session{ID: "s1"}, ok: true}
	r := newRouter(gate, lookup, &recordingNext{})

	params := permParams("s1")
	params["options"] = []map[string]string{{"kind": "allow_once", "optionId": "allow"}}
	resp, err := r.Handle(context.Background(), permReq(t, params))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if decode(t, resp).Outcome != "cancelled" {
		t.Errorf("deny with no reject option should cancel, got %+v", decode(t, resp))
	}
}

func TestApprovedPrefersAllowOnceOverAllowAlways(t *testing.T) {
	gate := &fakeApprover{outcome: approvals.Outcome{Approved: true}}
	lookup := fakeLookup{sess: &session.Session{ID: "s1"}, ok: true}
	r := newRouter(gate, lookup, &recordingNext{})

	// allow_always listed first; the router must still prefer allow_once so an
	// approval does not silently switch the session into a standing allow mode.
	params := permParams("s1")
	params["options"] = []map[string]string{
		{"kind": "allow_always", "optionId": "allow_always"},
		{"kind": "allow_once", "optionId": "allow"},
	}
	resp, err := r.Handle(context.Background(), permReq(t, params))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := decode(t, resp); got.OptionID != "allow" {
		t.Errorf("want allow_once optionId 'allow', got %+v", got)
	}
}

func TestApprovalGatedOnTurnContext(t *testing.T) {
	turnCtx, cancelTurn := context.WithCancel(context.Background())
	gate := &fakeApprover{outcome: approvals.Outcome{Approved: true}}
	lookup := fakeLookup{sess: &session.Session{ID: "s1"}, ok: true}
	turns := acp.TurnContextFunc(func(api.SessionID) (context.Context, bool) { return turnCtx, true })
	r := acp.NewPermissionRouter(&recordingNext{}, gate, lookup, turns)

	if _, err := r.Handle(context.Background(), permReq(t, permParams("s1"))); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	// The gate must have been handed the turn's context, so cancelling the turn
	// cancels the pending approval rather than leaving it bound to the connection.
	if gate.gotCtx == nil {
		t.Fatal("gate did not receive a context")
	}
	if gate.gotCtx.Err() != nil {
		t.Fatal("turn context should be live before cancel")
	}
	cancelTurn()
	if gate.gotCtx.Err() == nil {
		t.Error("cancelling the turn did not cancel the gate context")
	}
}

func TestApprovalFallsBackToConnContextWithoutTurn(t *testing.T) {
	connCtx, cancelConn := context.WithCancel(context.Background())
	gate := &fakeApprover{outcome: approvals.Outcome{Approved: true}}
	lookup := fakeLookup{sess: &session.Session{ID: "s1"}, ok: true}
	// No turn in flight → the bridge falls back to the request's connection ctx.
	turns := acp.TurnContextFunc(func(api.SessionID) (context.Context, bool) { return nil, false })
	r := acp.NewPermissionRouter(&recordingNext{}, gate, lookup, turns)

	if _, err := r.Handle(connCtx, permReq(t, permParams("s1"))); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	cancelConn()
	if gate.gotCtx == nil || gate.gotCtx.Err() == nil {
		t.Error("without a turn, the gate should use (and be cancelled by) the connection ctx")
	}
}

func TestApprovedFallsBackToAllowAlwaysWhenNoOnce(t *testing.T) {
	gate := &fakeApprover{outcome: approvals.Outcome{Approved: true}}
	lookup := fakeLookup{sess: &session.Session{ID: "s1"}, ok: true}
	r := newRouter(gate, lookup, &recordingNext{})

	params := permParams("s1")
	params["options"] = []map[string]string{{"kind": "allow_always", "optionId": "allow_always"}}
	resp, err := r.Handle(context.Background(), permReq(t, params))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := decode(t, resp); got.Outcome != "selected" || got.OptionID != "allow_always" {
		t.Errorf("want fallback to allow_always, got %+v", got)
	}
}
