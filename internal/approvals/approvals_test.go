package approvals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/events"
	"github.com/dusto/tend/internal/session"
)

func newGate(t *testing.T) (*Gate, *events.Store) {
	t.Helper()
	log, err := events.OpenLog(filepath.Join(t.TempDir(), "events.log"))
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	store := events.NewStore(log)
	g := NewGate(store, Options{NewID: func() api.ApprovalID { return "appr-1" }})
	return g, store
}

// runningSession returns a fresh session with a turn in flight, ready to gate.
func runningSession(t *testing.T) *session.Session {
	t.Helper()
	s := session.NewRegistry().Create("s1", "codex", "ws1", api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"}, "/repo")
	if err := s.SetStatus(api.StatusRunning, nil); err != nil {
		t.Fatalf("SetStatus running: %v", err)
	}
	return s
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

func TestRequestResolvedApproved(t *testing.T) {
	g, store := newGate(t)
	sess := runningSession(t)

	type result struct {
		out Outcome
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := g.Request(context.Background(), sess, "file_edit", json.RawMessage(`{"path":"a.go"}`))
		done <- result{out, err}
	}()

	// The gate marks the session waiting_approval with the pending approval id.
	waitFor(t, func() bool { _, ok := g.Get("appr-1"); return ok })
	if sess.Status() != api.StatusWaitingApproval {
		t.Fatalf("status = %q, want waiting_approval", sess.Status())
	}
	if p, ok := sess.Pending(); !ok || p.Kind != api.PendingApproval || p.ID != "appr-1" {
		t.Fatalf("pending = %+v, ok=%v", p, ok)
	}
	if list := g.List(); len(list) != 1 || list[0].Kind != "file_edit" || string(list[0].Detail) != `{"path":"a.go"}` {
		t.Fatalf("list = %+v", list)
	}

	if err := g.Resolve("appr-1", Decision{Approved: true}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	r := <-done
	if r.err != nil {
		t.Fatalf("Request: %v", r.err)
	}
	if !r.out.Approved {
		t.Errorf("outcome = %+v, want approved", r.out)
	}
	// Resolving returns the session to running and clears the pending interaction.
	if sess.Status() != api.StatusRunning {
		t.Errorf("status = %q, want running", sess.Status())
	}
	if _, ok := sess.Pending(); ok {
		t.Error("pending should be cleared after resolve")
	}
	if len(g.List()) != 0 {
		t.Error("gate should hold no pending approvals after resolve")
	}

	// approval_requested then approval_resolved on the repo-wide workspace stream
	// (not the session stream), scoped ScopeWorkspace.
	evs, _, err := store.Read("workspace:ws1", 0, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(evs) != 2 || evs[0].Type != "approval_requested" || evs[1].Type != "approval_resolved" {
		t.Fatalf("events = %+v", evs)
	}
	if evs[0].Scope != api.ScopeWorkspace || evs[1].Scope != api.ScopeWorkspace {
		t.Errorf("scopes = %q, %q, want workspace", evs[0].Scope, evs[1].Scope)
	}
	var resolved api.ApprovalResolved
	if err := json.Unmarshal(evs[1].Payload, &resolved); err != nil {
		t.Fatalf("unmarshal resolved: %v", err)
	}
	if !resolved.Approved || resolved.ApprovalID != "appr-1" || resolved.SessionID != "s1" {
		t.Errorf("resolved payload = %+v", resolved)
	}
}

func TestRequestResolvedDenied(t *testing.T) {
	g, _ := newGate(t)
	sess := runningSession(t)

	done := make(chan Outcome, 1)
	go func() {
		out, _ := g.Request(context.Background(), sess, "pane_run", nil)
		done <- out
	}()
	waitFor(t, func() bool { _, ok := g.Get("appr-1"); return ok })

	if err := g.Resolve("appr-1", Decision{Approved: false, Reason: "looks risky"}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	out := <-done
	if out.Approved || out.Reason != "looks risky" {
		t.Errorf("outcome = %+v, want denied with reason", out)
	}
	// A denied approval still returns the turn to running (the agent is told no).
	if sess.Status() != api.StatusRunning {
		t.Errorf("status = %q, want running", sess.Status())
	}
}

func TestPendingCarriesCreated(t *testing.T) {
	base := time.Unix(1000, 0)
	g := NewGate(nil, Options{
		NewID: func() api.ApprovalID { return "appr-1" },
		Now:   func() time.Time { return base },
	})
	sess := runningSession(t)
	go func() { _, _ = g.Request(context.Background(), sess, "file_edit", nil) }()
	waitFor(t, func() bool { _, ok := g.Get("appr-1"); return ok })

	// Approvals have no deadline (no TTL): the snapshot records only when it was
	// raised. See the no-TTL ADR.
	p, _ := g.Get("appr-1")
	if !p.Created.Equal(base) {
		t.Errorf("created = %v, want %v", p.Created, base)
	}
}

func TestResolveUnknown(t *testing.T) {
	g, _ := newGate(t)
	if err := g.Resolve("nope", Decision{Approved: true}); !errors.Is(err, ErrUnknownApproval) {
		t.Errorf("err = %v, want ErrUnknownApproval", err)
	}
}

func TestRequestRequiresRunning(t *testing.T) {
	g, _ := newGate(t)
	// An idle session has no in-flight turn: idle -> waiting_approval is illegal.
	sess := session.NewRegistry().Create("s1", "codex", "ws1", api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"}, "/repo")
	if _, err := g.Request(context.Background(), sess, "file_edit", nil); err == nil {
		t.Error("Request on an idle session should fail")
	}
	if len(g.List()) != 0 {
		t.Error("no pending approval should be recorded on failure")
	}
}

func TestResolveStaleSessionDeniesAndErrors(t *testing.T) {
	g, store := newGate(t)
	sess := runningSession(t)

	done := make(chan Outcome, 1)
	go func() {
		out, _ := g.Request(context.Background(), sess, "file_edit", nil)
		done <- out
	}()
	waitFor(t, func() bool { _, ok := g.Get("appr-1"); return ok })

	// The session leaves waiting_approval through another path (e.g. the turn was
	// ended) before the approval is answered.
	if err := sess.SetStatus(api.StatusEnded, nil); err != nil {
		t.Fatalf("SetStatus ended: %v", err)
	}

	// Approving now must not let the gated tool proceed: Resolve fails and the
	// waiter is unblocked with a denial.
	if err := g.Resolve("appr-1", Decision{Approved: true}); err == nil {
		t.Error("Resolve of a stale approval should return an error")
	}
	out := <-done
	if out.Approved {
		t.Errorf("stale approval delivered approved outcome: %+v", out)
	}
	if sess.Status() != api.StatusEnded {
		t.Errorf("status = %q, want ended (resolve must not force running)", sess.Status())
	}
	// The stale approval is unanswerable, so it is evicted on the stream too:
	// approval_requested then an eviction-shaped approval_resolved(Approved=false)
	// so subscribed clients clear it live, not only on the next approval.list sync.
	evs, _, _ := store.Read("workspace:ws1", 0, 10)
	if len(evs) != 2 || evs[0].Type != "approval_requested" || evs[1].Type != "approval_resolved" {
		t.Fatalf("events = %+v, want approval_requested then approval_resolved", evs)
	}
	var resolved api.ApprovalResolved
	if err := json.Unmarshal(evs[1].Payload, &resolved); err != nil {
		t.Fatalf("unmarshal resolved: %v", err)
	}
	if resolved.Approved || resolved.Reason == "" || resolved.ApprovalID != "appr-1" {
		t.Errorf("eviction payload = %+v, want Approved=false with a cause", resolved)
	}
}

func TestRequestCancelEvictsAndSignals(t *testing.T) {
	g, store := newGate(t)
	sess := runningSession(t)
	ctx, cancel := context.WithCancel(context.Background())

	errc := make(chan error, 1)
	go func() {
		_, err := g.Request(ctx, sess, "file_edit", nil)
		errc <- err
	}()
	waitFor(t, func() bool { _, ok := g.Get("appr-1"); return ok })

	// Cancelling the turn's context models any turn-death cause (provider crash,
	// agent.cancel, session end) — they all reach the gate as ctx cancellation.
	cancel()
	if err := <-errc; err == nil {
		t.Error("cancelled Request should return an error")
	}
	// The pending approval is dropped, so a late Resolve finds nothing.
	if len(g.List()) != 0 {
		t.Error("cancelled approval should be removed from the gate")
	}
	if err := g.Resolve("appr-1", Decision{Approved: true}); !errors.Is(err, ErrUnknownApproval) {
		t.Errorf("late Resolve err = %v, want ErrUnknownApproval", err)
	}

	// The eviction is signalled to clients: approval_requested then a synthetic
	// approval_resolved (Approved=false, cause in Reason) on the workspace stream,
	// so a subscribed UI clears the now-unanswerable approval live rather than
	// waiting for the next approval.list sync.
	evs, _, err := store.Read("workspace:ws1", 0, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(evs) != 2 || evs[0].Type != "approval_requested" || evs[1].Type != "approval_resolved" {
		t.Fatalf("events = %+v, want approval_requested then approval_resolved", evs)
	}
	if evs[1].Scope != api.ScopeWorkspace {
		t.Errorf("eviction scope = %q, want workspace", evs[1].Scope)
	}
	var resolved api.ApprovalResolved
	if err := json.Unmarshal(evs[1].Payload, &resolved); err != nil {
		t.Fatalf("unmarshal resolved: %v", err)
	}
	if resolved.Approved {
		t.Errorf("eviction Approved = true, want false (not a user denial)")
	}
	if resolved.Reason == "" {
		t.Error("eviction should carry the cause in Reason")
	}
	if resolved.ApprovalID != "appr-1" || resolved.SessionID != "s1" {
		t.Errorf("eviction payload = %+v, want appr-1/s1", resolved)
	}
}

// After an eviction, a resumed session that re-reaches the gated action raises a
// FRESH approval (a new id, a fresh approval_requested) — the gate is not left in
// a state that suppresses re-gating. A pending permission is in-agent turn state
// that cannot survive a provider death, so "resume where we left off" means the
// session re-runs and re-gates, not that the same approval object is restored.
func TestRequestReRaisesAfterEviction(t *testing.T) {
	var n int
	ids := func() api.ApprovalID {
		n++
		return api.ApprovalID(fmt.Sprintf("appr-%d", n))
	}
	log, err := events.OpenLog(filepath.Join(t.TempDir(), "events.log"))
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	store := events.NewStore(log)
	g := NewGate(store, Options{NewID: ids})

	// First gated action, then the turn dies (ctx cancelled): approval evicted.
	sess := runningSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { _, e := g.Request(ctx, sess, "file_edit", nil); errc <- e }()
	waitFor(t, func() bool { _, ok := g.Get("appr-1"); return ok })
	cancel()
	<-errc

	// The resumed session (a fresh session, as resume_seed creates one) re-reaches
	// the gated action and raises a brand-new approval.
	resumed := runningSession(t)
	go func() { _, _ = g.Request(context.Background(), resumed, "file_edit", nil) }()
	waitFor(t, func() bool { _, ok := g.Get("appr-2"); return ok })

	if _, ok := g.Get("appr-1"); ok {
		t.Error("the evicted approval must not linger")
	}
	if len(g.List()) != 1 {
		t.Fatalf("pending = %d, want exactly the fresh approval", len(g.List()))
	}
	// The stream carries the fresh approval_requested (appr-2) after the eviction.
	evs, _, err := store.Read("workspace:ws1", 0, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	last := evs[len(evs)-1]
	var req api.ApprovalRequested
	if err := json.Unmarshal(last.Payload, &req); err != nil {
		t.Fatalf("unmarshal requested: %v", err)
	}
	if last.Type != "approval_requested" || req.ApprovalID != "appr-2" {
		t.Errorf("last event = %+v (%+v), want a fresh approval_requested appr-2", last, req)
	}
}
