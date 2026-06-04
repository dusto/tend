package approvals

import (
	"context"
	"encoding/json"
	"errors"
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
	s := session.NewRegistry().Create("s1", "codex", api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"}, "/repo")
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

	// approval_requested then approval_resolved on the session stream.
	evs, _, err := store.Read("session:s1", 0, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(evs) != 2 || evs[0].Type != "approval_requested" || evs[1].Type != "approval_resolved" {
		t.Fatalf("events = %+v", evs)
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

func TestResolveUnknown(t *testing.T) {
	g, _ := newGate(t)
	if err := g.Resolve("nope", Decision{Approved: true}); !errors.Is(err, ErrUnknownApproval) {
		t.Errorf("err = %v, want ErrUnknownApproval", err)
	}
}

func TestRequestRequiresRunning(t *testing.T) {
	g, _ := newGate(t)
	// An idle session has no in-flight turn: idle -> waiting_approval is illegal.
	sess := session.NewRegistry().Create("s1", "codex", api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"}, "/repo")
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
	// Only approval_requested was raised; no approval_resolved for a stale resolve.
	evs, _, _ := store.Read("session:s1", 0, 10)
	if len(evs) != 1 || evs[0].Type != "approval_requested" {
		t.Errorf("events = %+v, want only approval_requested", evs)
	}
}

func TestRequestCancelledDropsPending(t *testing.T) {
	g, _ := newGate(t)
	sess := runningSession(t)
	ctx, cancel := context.WithCancel(context.Background())

	errc := make(chan error, 1)
	go func() {
		_, err := g.Request(ctx, sess, "file_edit", nil)
		errc <- err
	}()
	waitFor(t, func() bool { _, ok := g.Get("appr-1"); return ok })

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
}
