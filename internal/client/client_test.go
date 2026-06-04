package client

import (
	"context"
	"testing"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/dispatch"
)

func TestRegistryRegisterGetRemove(t *testing.T) {
	r := NewRegistry()
	ed := r.Register("c1", Capabilities{Role: api.RoleEditor, PromptCapable: true}, nil)
	r.Register("c2", Capabilities{Role: api.RoleObserver}, nil)

	if !ed.IsEditor() || !ed.CanRespondToPrompts() {
		t.Errorf("editor caps = %+v", ed.Caps)
	}
	if obs, _ := r.Get("c2"); obs.IsEditor() || obs.CanRespondToPrompts() {
		t.Errorf("observer caps = %+v", obs.Caps)
	}
	if len(r.List()) != 2 {
		t.Errorf("list len = %d, want 2", len(r.List()))
	}

	r.Remove("c1")
	if _, ok := r.Get("c1"); ok {
		t.Error("c1 should be gone after Remove")
	}
	if len(r.List()) != 1 {
		t.Errorf("list len = %d, want 1", len(r.List()))
	}
}

func TestRegisterReplacesOnReconnect(t *testing.T) {
	r := NewRegistry()
	r.Register("c1", Capabilities{Role: api.RoleObserver}, nil)
	r.Register("c1", Capabilities{Role: api.RoleEditor, PromptCapable: true}, nil)

	c, ok := r.Get("c1")
	if !ok || !c.IsEditor() || !c.CanRespondToPrompts() {
		t.Errorf("re-registered client = %+v, ok=%v", c, ok)
	}
	if len(r.List()) != 1 {
		t.Errorf("list len = %d, want 1 (same id)", len(r.List()))
	}
}

// newConn registers a Conn's handler on a fresh mux and returns both.
func newConn(t *testing.T, r *Registry) (*Conn, *dispatch.Mux) {
	t.Helper()
	c := NewConn(r)
	mux := dispatch.NewMux(api.PluginToDaemon)
	if err := c.Register(mux); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return c, mux
}

func TestConnRegistersIdentity(t *testing.T) {
	r := NewRegistry()
	c, _ := newConn(t, r)

	res, err := c.register(context.Background(), api.ClientRegisterParams{
		ClientID: "c1", Role: api.RoleEditor, PromptCapable: true,
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if res.ClientID != "c1" {
		t.Errorf("result client id = %q", res.ClientID)
	}
	// The identity is reachable both on the connection and in the shared registry.
	if self, ok := c.Self(); !ok || self.ID != "c1" || !self.IsEditor() {
		t.Errorf("self = %+v, ok=%v", self, ok)
	}
	if _, ok := r.Get("c1"); !ok {
		t.Error("registry should hold c1")
	}
}

func TestConnRegisterValidates(t *testing.T) {
	c, _ := newConn(t, NewRegistry())
	cases := []api.ClientRegisterParams{
		{Role: api.RoleEditor},                        // no client id
		{ClientID: "c1"},                              // no role
		{ClientID: "c1", Role: api.ClientRole("xyz")}, // unknown role
	}
	for i, p := range cases {
		if _, err := c.register(context.Background(), p); err == nil {
			t.Errorf("case %d: expected validation error", i)
		}
	}
	if _, ok := c.Self(); ok {
		t.Error("no identity should be recorded on failed registration")
	}
}

func TestConnCloseRemovesIdentity(t *testing.T) {
	r := NewRegistry()
	c, _ := newConn(t, r)
	if _, err := c.register(context.Background(), api.ClientRegisterParams{ClientID: "c1", Role: api.RoleObserver}); err != nil {
		t.Fatalf("register: %v", err)
	}

	if !c.Close() {
		t.Error("Close should report it removed the registered identity")
	}
	if _, ok := r.Get("c1"); ok {
		t.Error("client should be removed from the registry on connection close")
	}
}

func TestConnCloseWithoutRegisterIsSafe(t *testing.T) {
	c, _ := newConn(t, NewRegistry())
	if c.Close() { // must not panic, and reports nothing was removed
		t.Error("Close without a prior register should remove nothing")
	}
}

func TestReconnectStaleCloseKeepsLiveIdentity(t *testing.T) {
	r := NewRegistry()
	old, _ := newConn(t, r)
	if _, err := old.register(context.Background(), api.ClientRegisterParams{ClientID: "c1", Role: api.RoleObserver}); err != nil {
		t.Fatalf("old register: %v", err)
	}
	// The same id reconnects on a new connection (e.g. as an editor) before the
	// old connection's teardown runs.
	fresh, _ := newConn(t, r)
	if _, err := fresh.register(context.Background(), api.ClientRegisterParams{ClientID: "c1", Role: api.RoleEditor, PromptCapable: true}); err != nil {
		t.Fatalf("fresh register: %v", err)
	}

	// Stale teardown must not evict the live replacement and must report it did
	// not remove the entry (so callers skip owner-only teardown).
	if old.Close() {
		t.Error("stale connection Close should report it removed nothing")
	}
	cl, ok := r.Get("c1")
	if !ok {
		t.Fatal("live identity removed by stale connection close")
	}
	if !cl.IsEditor() || !cl.CanRespondToPrompts() {
		t.Errorf("registry holds wrong identity after stale close: %+v", cl.Caps)
	}

	if !fresh.Close() { // the owning connection does remove it
		t.Error("owning connection Close should report it removed the entry")
	}
	if _, ok := r.Get("c1"); ok {
		t.Error("identity should be gone after the owning connection closes")
	}
}

func TestReRegisterDifferentIDReleasesPrevious(t *testing.T) {
	r := NewRegistry()
	c, _ := newConn(t, r)
	if _, err := c.register(context.Background(), api.ClientRegisterParams{ClientID: "c1", Role: api.RoleObserver}); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if _, err := c.register(context.Background(), api.ClientRegisterParams{ClientID: "c2", Role: api.RoleEditor}); err != nil {
		t.Fatalf("second register: %v", err)
	}
	// The previous identity is released; only the new one remains.
	if _, ok := r.Get("c1"); ok {
		t.Error("previous identity c1 should be released on re-register")
	}
	if _, ok := r.Get("c2"); !ok {
		t.Error("registry should hold the new identity c2")
	}
	if self, ok := c.Self(); !ok || self.ID != "c2" {
		t.Errorf("self = %+v, ok=%v, want c2", self, ok)
	}
}
