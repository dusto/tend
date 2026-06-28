package session

import (
	"testing"

	"github.com/dusto/tend/api"
)

func newSession(t *testing.T) *Session {
	t.Helper()
	r := NewRegistry()
	return r.Create("s1", "codex", "ws1", api.TaskRef{Provider: "beads", ID: "t1"}, "/repo")
}

func TestCreateInitialState(t *testing.T) {
	s := newSession(t)
	if s.Status() != api.StatusIdle {
		t.Errorf("status = %q, want idle", s.Status())
	}
	if s.Stream != "session:s1" {
		t.Errorf("stream = %q, want session:s1", s.Stream)
	}
	if s.WorktreeRoot != "/repo" || s.ProviderID != "codex" {
		t.Errorf("session = %+v", s)
	}
	if _, ok := s.Pending(); ok {
		t.Error("new session should have no pending interaction")
	}
}

func TestLegalTurnLifecycle(t *testing.T) {
	s := newSession(t)
	steps := []api.SessionStatus{
		api.StatusRunning, // start a turn
		api.StatusIdle,    // turn ends
		api.StatusRunning, // another turn
		api.StatusEnded,   // session ends
	}
	for _, to := range steps {
		if err := s.SetStatus(to, nil); err != nil {
			t.Fatalf("transition to %s: %v", to, err)
		}
	}
	if s.Status() != api.StatusEnded {
		t.Errorf("final status = %q, want ended", s.Status())
	}
}

func TestIllegalTransitionsRejected(t *testing.T) {
	s := newSession(t)
	// idle -> waiting_approval is illegal (must be running first).
	if err := s.SetStatus(api.StatusWaitingApproval, &Pending{Kind: api.PendingApproval, ID: "a1"}); err == nil {
		t.Error("idle -> waiting_approval should be illegal")
	}
	// ended is terminal.
	mustTransition(t, s, api.StatusRunning, nil)
	mustTransition(t, s, api.StatusEnded, nil)
	if err := s.SetStatus(api.StatusRunning, nil); err == nil {
		t.Error("ended -> running should be illegal (terminal)")
	}
}

func TestWaitingRequiresMatchingPending(t *testing.T) {
	s := newSession(t)
	mustTransition(t, s, api.StatusRunning, nil)

	// Missing pending.
	if err := s.SetStatus(api.StatusWaitingApproval, nil); err == nil {
		t.Error("waiting_approval without pending should fail")
	}
	// Wrong kind.
	if err := s.SetStatus(api.StatusWaitingApproval, &Pending{Kind: api.PendingClarification, ID: "c1"}); err == nil {
		t.Error("waiting_approval with a clarification pending should fail")
	}
	// Empty id: cannot be correlated to an approval.
	if err := s.SetStatus(api.StatusWaitingApproval, &Pending{Kind: api.PendingApproval}); err == nil {
		t.Error("waiting_approval with an empty pending id should fail")
	}
	// Correct.
	mustTransition(t, s, api.StatusWaitingApproval, &Pending{Kind: api.PendingApproval, ID: "a1"})
	if p, ok := s.Pending(); !ok || p.Kind != api.PendingApproval || p.ID != "a1" {
		t.Errorf("pending = %+v, %v", p, ok)
	}
}

func TestLeavingWaitingClearsPending(t *testing.T) {
	s := newSession(t)
	mustTransition(t, s, api.StatusRunning, nil)
	mustTransition(t, s, api.StatusWaitingClarification, &Pending{Kind: api.PendingClarification, ID: "c1"})
	mustTransition(t, s, api.StatusRunning, nil) // clarification answered
	if _, ok := s.Pending(); ok {
		t.Error("pending should be cleared after leaving waiting")
	}
}

func mustTransition(t *testing.T, s *Session, to api.SessionStatus, p *Pending) {
	t.Helper()
	if err := s.SetStatus(to, p); err != nil {
		t.Fatalf("transition to %s: %v", to, err)
	}
}

func TestModeAndModelState(t *testing.T) {
	s := newSession(t)

	// Defaults: no modes/models until recorded.
	if cur, modes := s.Modes(); cur != "" || modes != nil {
		t.Errorf("default modes = %q %+v", cur, modes)
	}

	s.SetModes("default", []api.SessionMode{{ID: "default"}, {ID: "think"}})
	s.SetModels("sonnet", []api.SessionModel{{ID: "sonnet"}, {ID: "opus"}})

	if cur, modes := s.Modes(); cur != "default" || len(modes) != 2 {
		t.Errorf("modes = %q %+v", cur, modes)
	}
	if cur, models := s.Models(); cur != "sonnet" || len(models) != 2 {
		t.Errorf("models = %q %+v", cur, models)
	}

	// Current-only updates leave the available lists intact.
	s.SetCurrentMode("think")
	s.SetCurrentModel("opus")
	if cur, modes := s.Modes(); cur != "think" || len(modes) != 2 {
		t.Errorf("after SetCurrentMode = %q %+v", cur, modes)
	}
	if cur, models := s.Models(); cur != "opus" || len(models) != 2 {
		t.Errorf("after SetCurrentModel = %q %+v", cur, models)
	}
}
