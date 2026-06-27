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
