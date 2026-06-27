package harness

import (
	"path/filepath"
	"testing"

	"github.com/dusto/tend/api"
)

// registerEditor dials a fresh connection and registers it as an editor with the
// given client id.
func registerEditor(t *testing.T, sock, id string) *Client {
	t.Helper()
	c := dial(t, sock)
	mustCall(t, c, "daemon.hello", api.HelloParams{}, &api.HelloResult{})
	mustCall(t, c, "client.register", api.ClientRegisterParams{ClientID: api.ClientID(id), Role: api.RoleEditor, PromptCapable: true}, &api.ClientRegisterResult{})
	return c
}

// TestSessionListRichFields drives session.list over the socket: two sessions
// started by one editor are both listed with task/status/stream, both held by
// their creator (per-session binding), and the workspace filter narrows the set.
func TestSessionListRichFields(t *testing.T) {
	sock := fakeDaemon(t)
	c := registerEditor(t, sock, "ed")

	rootA, rootB := t.TempDir(), t.TempDir()
	var a api.AgentStartResult
	mustCall(t, c, "agent.start", api.AgentStartParams{ProviderID: "codex", Task: api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"}, WorktreeRoot: rootA}, &a)
	var b api.AgentStartResult
	mustCall(t, c, "agent.start", api.AgentStartParams{ProviderID: "codex", Task: api.TaskRef{Provider: "beads", WorkspaceID: "ws2", ID: "t2"}, WorktreeRoot: rootB}, &b)

	var list api.SessionListResult
	mustCall(t, c, "session.list", api.SessionListParams{}, &list)
	if len(list.Sessions) != 2 {
		t.Fatalf("session.list = %d, want 2", len(list.Sessions))
	}
	byID := map[api.SessionID]api.SessionInfo{}
	for _, s := range list.Sessions {
		byID[s.SessionID] = s
	}
	if got := byID[a.SessionID]; got.Task.ID != "t1" || got.StreamID != a.StreamID || got.WorktreeRoot != rootA {
		t.Errorf("session a = %+v, want its task/stream/worktree", got)
	}
	// Per-session binding: the editor that started both owns both.
	if !byID[a.SessionID].EditorBound || !byID[b.SessionID].EditorBound {
		t.Errorf("editor_bound a:%v b:%v, want both true for their creator",
			byID[a.SessionID].EditorBound, byID[b.SessionID].EditorBound)
	}

	// Workspace filter narrows to one.
	mustCall(t, c, "session.list", api.SessionListParams{WorkspaceID: "ws2"}, &list)
	if len(list.Sessions) != 1 || list.Sessions[0].SessionID != b.SessionID {
		t.Fatalf("filtered = %+v, want only b (ws2)", list.Sessions)
	}
}

// TestSessionClaimHandoff proves session.claim moves a session's editor binding
// to the claiming client, so editor-fresh reads route to the new owner and both
// clients' session.list reflect the handoff.
func TestSessionClaimHandoff(t *testing.T) {
	sock := fakeDaemon(t)
	ed := registerEditor(t, sock, "ed")

	root := t.TempDir()
	var s api.AgentStartResult
	mustCall(t, ed, "agent.start", api.AgentStartParams{ProviderID: "codex", Task: api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"}, WorktreeRoot: root}, &s)

	// ed created and so owns the session; an editor-fresh read routes to ed.
	uri := fileURI(filepath.Join(root, "f.go"))
	ed.SetOpenBuffer(uri, "from ed")
	var r api.FileReadResult
	mustCall(t, ed, "file.read", api.FileReadParams{SessionID: s.SessionID, URI: uri}, &r)
	if r.Content != "from ed" {
		t.Fatalf("read before claim = %q, want ed's buffer", r.Content)
	}

	// A second editor claims the session: the binding moves to ed2.
	ed2 := registerEditor(t, sock, "ed2")
	var claimed api.SessionClaimResult
	mustCall(t, ed2, "session.claim", api.SessionClaimParams{SessionID: s.SessionID}, &claimed)
	if !claimed.Session.EditorBound {
		t.Fatalf("claim result not bound: %+v", claimed.Session)
	}

	// Now an editor-fresh read routes to ed2 (its buffer), not ed's.
	ed2.SetOpenBuffer(uri, "from ed2")
	mustCall(t, ed2, "file.read", api.FileReadParams{SessionID: s.SessionID, URI: uri}, &r)
	if r.Content != "from ed2" {
		t.Fatalf("read after claim = %q, want ed2's buffer", r.Content)
	}

	// Each client's list reflects the handoff: ed2 bound, ed not.
	var l2 api.SessionListResult
	mustCall(t, ed2, "session.list", api.SessionListParams{}, &l2)
	if !l2.Sessions[0].EditorBound {
		t.Error("ed2 should see editor_bound after claiming")
	}
	var l1 api.SessionListResult
	mustCall(t, ed, "session.list", api.SessionListParams{}, &l1)
	if l1.Sessions[0].EditorBound {
		t.Error("ed should no longer see editor_bound after ed2 claimed")
	}
}
