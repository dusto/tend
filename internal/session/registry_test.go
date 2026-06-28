package session

import (
	"testing"

	"github.com/dusto/tend/api"
)

func TestRegistryCreateGetRemove(t *testing.T) {
	r := NewRegistry()
	s := r.Create("s1", "codex", "ws1", api.TaskRef{}, "/repo")

	got, ok := r.Get("s1")
	if !ok || got != s {
		t.Fatalf("Get(s1) = %v, %v", got, ok)
	}
	if _, ok := r.Get("missing"); ok {
		t.Error("Get(missing) should be false")
	}

	r.Remove("s1")
	if _, ok := r.Get("s1"); ok {
		t.Error("session present after Remove")
	}
}

func TestRegistryList(t *testing.T) {
	r := NewRegistry()
	r.Create("s1", "codex", "ws1", api.TaskRef{}, "/a")
	r.Create("s2", "claude", "ws1", api.TaskRef{}, "/b")
	if n := len(r.List()); n != 2 {
		t.Errorf("List len = %d, want 2", n)
	}
}

func TestRegistryDuplicatePanics(t *testing.T) {
	r := NewRegistry()
	r.Create("s1", "codex", "ws1", api.TaskRef{}, "/a")
	defer func() {
		if recover() == nil {
			t.Error("duplicate Create should panic")
		}
	}()
	r.Create("s1", "codex", "ws1", api.TaskRef{}, "/a")
}

func TestRegistrySetSessionMode(t *testing.T) {
	r := NewRegistry()
	s := r.Create("s1", "codex", "ws1", api.TaskRef{}, "/repo")
	s.SetModes("default", []api.SessionMode{{ID: "default"}, {ID: "think"}})

	// An agent-driven mode change is recorded on the session.
	r.SetSessionMode("s1", "think")
	if cur, _ := s.Modes(); cur != "think" {
		t.Errorf("current mode = %q, want think", cur)
	}

	// An unknown session id is a no-op, not a panic.
	r.SetSessionMode("missing", "x")
}
