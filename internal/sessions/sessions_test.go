package sessions

import (
	"errors"
	"testing"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/client"
	"github.com/dusto/tend/internal/editor"
	"github.com/dusto/tend/internal/session"
)

// fixture builds a sessions service over a registry + binder, with one editor
// client "ed" registered, and returns the service and registries.
func fixture(t *testing.T) (*Service, *session.Registry, *client.Registry, *editor.Binder) {
	t.Helper()
	sess := session.NewRegistry()
	clients := client.NewRegistry()
	clients.Register("ed", client.Capabilities{Role: api.RoleEditor}, nil)
	binder := editor.NewBinder(sess, clients)
	return NewService(sess, binder), sess, clients, binder
}

func ref(ws, id string) api.TaskRef {
	return api.TaskRef{Provider: "beads", WorkspaceID: api.WorkspaceID(ws), ID: id}
}

func TestListReportsRichFieldsOrderedByID(t *testing.T) {
	svc, sess, _, _ := fixture(t)
	sess.Create("s2", "codex", ref("ws1", "t2"), "/repo/b")
	a := sess.Create("s1", "codex", ref("ws1", "t1"), "/repo/a")
	// s1 is waiting on an approval.
	if err := a.SetStatus(api.StatusRunning, nil); err != nil {
		t.Fatalf("running: %v", err)
	}
	if err := a.SetStatus(api.StatusWaitingApproval, &session.Pending{Kind: api.PendingApproval, ID: "ap-1"}); err != nil {
		t.Fatalf("waiting: %v", err)
	}

	res := svc.List("ed", api.SessionListParams{})
	if len(res.Sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(res.Sessions))
	}
	// Ordered by id: s1 then s2.
	if res.Sessions[0].SessionID != "s1" || res.Sessions[1].SessionID != "s2" {
		t.Fatalf("order = %s,%s, want s1,s2", res.Sessions[0].SessionID, res.Sessions[1].SessionID)
	}
	s1 := res.Sessions[0]
	if s1.ProviderID != "codex" || s1.Task.ID != "t1" || s1.WorktreeRoot != "/repo/a" || s1.StreamID == "" {
		t.Errorf("s1 fields = %+v", s1)
	}
	if s1.Status != api.StatusWaitingApproval || s1.Pending == nil || s1.Pending.Kind != api.PendingApproval || s1.Pending.ID != "ap-1" {
		t.Errorf("s1 status/pending = %+v / %+v", s1.Status, s1.Pending)
	}
	// s2 is idle with no pending.
	if res.Sessions[1].Status != api.StatusIdle || res.Sessions[1].Pending != nil {
		t.Errorf("s2 status/pending = %+v / %+v", res.Sessions[1].Status, res.Sessions[1].Pending)
	}
}

func TestListFiltersByWorkspace(t *testing.T) {
	svc, sess, _, _ := fixture(t)
	sess.Create("s1", "codex", ref("ws1", "t1"), "/a")
	sess.Create("s2", "codex", ref("ws2", "t2"), "/b")

	res := svc.List("ed", api.SessionListParams{WorkspaceID: "ws2"})
	if len(res.Sessions) != 1 || res.Sessions[0].SessionID != "s2" {
		t.Fatalf("filtered = %+v, want only s2", res.Sessions)
	}
}

func TestListReportsEditorBindingRelativeToCaller(t *testing.T) {
	svc, sess, _, binder := fixture(t)
	sess.Create("s1", "codex", ref("ws1", "t1"), "/a")
	if err := binder.Claim("s1", "ed"); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// The binding owner sees editor_bound; a different caller does not.
	if got := svc.List("ed", api.SessionListParams{}); !got.Sessions[0].EditorBound {
		t.Error("owner should see editor_bound=true")
	}
	if got := svc.List("other", api.SessionListParams{}); got.Sessions[0].EditorBound {
		t.Error("non-owner should see editor_bound=false")
	}
}

func TestClaimMovesBindingAndReturnsView(t *testing.T) {
	svc, sess, _, binder := fixture(t)
	sess.Create("s1", "codex", ref("ws1", "t1"), "/a")

	res, err := svc.Claim("ed", api.SessionClaimParams{SessionID: "s1"})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if !res.Session.EditorBound || res.Session.SessionID != "s1" {
		t.Errorf("claim result = %+v, want s1 bound to caller", res.Session)
	}
	// The binding actually moved.
	owner, bound := mustGet(t, sess, "s1").Owner()
	if !bound || owner != "ed" {
		t.Errorf("binding owner = %q bound=%v, want ed", owner, bound)
	}
	_ = binder
}

func TestClaimByObserverRejected(t *testing.T) {
	svc, sess, clients, _ := fixture(t)
	clients.Register("obs", client.Capabilities{Role: api.RoleObserver}, nil)
	sess.Create("s1", "codex", ref("ws1", "t1"), "/a")

	if _, err := svc.Claim("obs", api.SessionClaimParams{SessionID: "s1"}); !errors.Is(err, editor.ErrNotEditor) {
		t.Errorf("observer claim err = %v, want ErrNotEditor", err)
	}
}

func TestClaimUnknownSession(t *testing.T) {
	svc, _, _, _ := fixture(t)
	if _, err := svc.Claim("ed", api.SessionClaimParams{SessionID: "nope"}); !errors.Is(err, editor.ErrNoSession) {
		t.Errorf("claim unknown err = %v, want ErrNoSession", err)
	}
}

func mustGet(t *testing.T, r *session.Registry, id api.SessionID) *session.Session {
	t.Helper()
	s, ok := r.Get(id)
	if !ok {
		t.Fatalf("session %s missing", id)
	}
	return s
}
