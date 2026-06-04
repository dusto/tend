package editor

import (
	"errors"
	"testing"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/client"
	"github.com/dusto/tend/internal/session"
)

type fixture struct {
	binder   *Binder
	sessions *session.Registry
	clients  *client.Registry
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	sessions := session.NewRegistry()
	sessions.Create("s1", "codex", api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"}, "/repo")
	clients := client.NewRegistry()
	clients.Register("ed1", client.Capabilities{Role: api.RoleEditor, PromptCapable: true})
	clients.Register("ed2", client.Capabilities{Role: api.RoleEditor})
	clients.Register("obs", client.Capabilities{Role: api.RoleObserver})
	return fixture{binder: NewBinder(sessions, clients), sessions: sessions, clients: clients}
}

func TestClaimBindsEditor(t *testing.T) {
	f := newFixture(t)
	if err := f.binder.Claim("s1", "ed1"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	owner, err := f.binder.Owner("s1")
	if err != nil || owner != "ed1" {
		t.Fatalf("owner = %q, err = %v", owner, err)
	}
}

func TestClaimRequiresEditorCapableClient(t *testing.T) {
	f := newFixture(t)
	if err := f.binder.Claim("s1", "obs"); !errors.Is(err, ErrNotEditor) {
		t.Errorf("observer claim err = %v, want ErrNotEditor", err)
	}
	if err := f.binder.Claim("s1", "ghost"); !errors.Is(err, ErrNotEditor) {
		t.Errorf("unknown-client claim err = %v, want ErrNotEditor", err)
	}
	if err := f.binder.Claim("missing", "ed1"); !errors.Is(err, ErrNoSession) {
		t.Errorf("unknown-session claim err = %v, want ErrNoSession", err)
	}
}

func TestClaimTakesOver(t *testing.T) {
	f := newFixture(t)
	if err := f.binder.Claim("s1", "ed1"); err != nil {
		t.Fatalf("Claim ed1: %v", err)
	}
	if err := f.binder.Claim("s1", "ed2"); err != nil {
		t.Fatalf("Claim ed2: %v", err)
	}
	if owner, _ := f.binder.Owner("s1"); owner != "ed2" {
		t.Errorf("owner = %q, want ed2 after takeover", owner)
	}
}

func TestOwnerHeadlessIsUnavailable(t *testing.T) {
	f := newFixture(t)
	if _, err := f.binder.Owner("s1"); !errors.Is(err, ErrEditorUnavailable) {
		t.Errorf("headless Owner err = %v, want ErrEditorUnavailable", err)
	}
	if _, err := f.binder.Owner("missing"); !errors.Is(err, ErrNoSession) {
		t.Errorf("unknown-session Owner err = %v, want ErrNoSession", err)
	}
}

func TestAttachIdentityChecked(t *testing.T) {
	f := newFixture(t)
	// Establish ed1 as the session's editor, then disconnect it (headless).
	if err := f.binder.Claim("s1", "ed1"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	f.binder.ReleaseClient("ed1")
	if _, err := f.binder.Owner("s1"); !errors.Is(err, ErrEditorUnavailable) {
		t.Fatalf("after release, Owner err = %v, want ErrEditorUnavailable", err)
	}

	// A different editor must not auto-capture the session.
	bound, err := f.binder.Attach("s1", "ed2")
	if err != nil {
		t.Fatalf("Attach ed2: %v", err)
	}
	if bound {
		t.Error("non-matching editor should not auto-bind")
	}

	// The original editor reattaches and auto-binds.
	bound, err = f.binder.Attach("s1", "ed1")
	if err != nil {
		t.Fatalf("Attach ed1: %v", err)
	}
	if !bound {
		t.Error("matching editor should auto-bind")
	}
	if owner, _ := f.binder.Owner("s1"); owner != "ed1" {
		t.Errorf("owner = %q, want ed1", owner)
	}
}

func TestAttachRequiresEditorCapableClient(t *testing.T) {
	f := newFixture(t)
	if _, err := f.binder.Attach("s1", "obs"); !errors.Is(err, ErrNotEditor) {
		t.Errorf("observer attach err = %v, want ErrNotEditor", err)
	}
}

func TestReleaseClientOnlyAffectsItsBindings(t *testing.T) {
	f := newFixture(t)
	f.sessions.Create("s2", "codex", api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t2"}, "/repo")
	if err := f.binder.Claim("s1", "ed1"); err != nil {
		t.Fatalf("Claim s1: %v", err)
	}
	if err := f.binder.Claim("s2", "ed2"); err != nil {
		t.Fatalf("Claim s2: %v", err)
	}

	f.binder.ReleaseClient("ed1")

	if _, err := f.binder.Owner("s1"); !errors.Is(err, ErrEditorUnavailable) {
		t.Errorf("s1 should be headless after ed1 disconnect, err = %v", err)
	}
	if owner, err := f.binder.Owner("s2"); err != nil || owner != "ed2" {
		t.Errorf("s2 owner = %q, err = %v, want ed2 unaffected", owner, err)
	}
}
