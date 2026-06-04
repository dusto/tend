package session

import (
	"testing"
)

func TestNewSessionIsHeadless(t *testing.T) {
	s := newSession(t)
	if owner, bound := s.Owner(); bound || owner != "" {
		t.Errorf("new session owner = %q, bound=%v, want headless", owner, bound)
	}
	if s.ExpectedEditor() != "" {
		t.Errorf("expected editor = %q, want empty", s.ExpectedEditor())
	}
}

func TestBindOwnerRecordsExpectedEditor(t *testing.T) {
	s := newSession(t)
	s.BindOwner("ed1")
	if owner, bound := s.Owner(); !bound || owner != "ed1" {
		t.Errorf("owner = %q, bound=%v, want ed1", owner, bound)
	}
	if s.ExpectedEditor() != "ed1" {
		t.Errorf("expected editor = %q, want ed1", s.ExpectedEditor())
	}
}

func TestBindOwnerTakesOver(t *testing.T) {
	s := newSession(t)
	s.BindOwner("ed1")
	s.BindOwner("ed2") // deliberate takeover
	if owner, _ := s.Owner(); owner != "ed2" {
		t.Errorf("owner = %q, want ed2 after takeover", owner)
	}
	if s.ExpectedEditor() != "ed2" {
		t.Errorf("expected editor = %q, want ed2", s.ExpectedEditor())
	}
}

func TestAutoBindOwnerMatchingOnly(t *testing.T) {
	s := newSession(t)
	// No expected editor recorded yet: auto-bind must refuse (nothing to match).
	if s.AutoBindOwner("ed1") {
		t.Error("auto-bind should refuse when no expected editor is recorded")
	}

	// Record an expected editor, then go headless.
	s.BindOwner("ed1")
	if !s.ReleaseOwner("ed1") {
		t.Fatal("ReleaseOwner should release the current owner")
	}

	// A different editor must not capture the session.
	if s.AutoBindOwner("ed2") {
		t.Error("auto-bind should refuse a non-matching editor")
	}
	if _, bound := s.Owner(); bound {
		t.Error("session should remain headless after a non-matching auto-bind")
	}

	// The matching editor reattaches and auto-binds.
	if !s.AutoBindOwner("ed1") {
		t.Error("auto-bind should accept the matching editor")
	}
	if owner, _ := s.Owner(); owner != "ed1" {
		t.Errorf("owner = %q, want ed1", owner)
	}
}

func TestAutoBindOwnerRefusesWhenBound(t *testing.T) {
	s := newSession(t)
	s.BindOwner("ed1")
	if s.AutoBindOwner("ed1") {
		t.Error("auto-bind should refuse when the session is already bound")
	}
}

func TestReleaseOwnerIsOwnershipChecked(t *testing.T) {
	s := newSession(t)
	s.BindOwner("ed1")
	if s.ReleaseOwner("ed2") {
		t.Error("ReleaseOwner by a non-owner should not release")
	}
	if owner, _ := s.Owner(); owner != "ed1" {
		t.Errorf("owner = %q, want ed1 (unchanged)", owner)
	}
	// Releasing retains the expected editor for reattach.
	if !s.ReleaseOwner("ed1") {
		t.Fatal("ReleaseOwner by the owner should release")
	}
	if s.ExpectedEditor() != "ed1" {
		t.Errorf("expected editor = %q, want ed1 retained", s.ExpectedEditor())
	}
}
