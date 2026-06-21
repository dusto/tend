package harness

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/rpc"
)

// fileURI builds a file:// uri for an absolute path.
func fileURI(path string) string {
	return "file://" + path
}

// startAs registers a client with the given role and starts a session, returning
// the client, the start result, and the worktree root the session runs in.
func startAs(t *testing.T, sock string, role api.ClientRole) (*Client, api.AgentStartResult, string) {
	t.Helper()
	c := dial(t, sock)
	mustCall(t, c, "daemon.hello", api.HelloParams{}, &api.HelloResult{})
	mustCall(t, c, "client.register", api.ClientRegisterParams{ClientID: "ed", Role: role, PromptCapable: true}, &api.ClientRegisterResult{})
	root := t.TempDir()
	var started api.AgentStartResult
	mustCall(t, c, "agent.start", api.AgentStartParams{
		ProviderID:   "codex",
		Task:         api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"},
		WorktreeRoot: root,
	}, &started)
	return c, started, root
}

// TestEditorClientIsBoundOnStart proves agent.start binds the creating editor:
// editor-local reverse calls for the session route back to that client.
func TestEditorClientIsBoundOnStart(t *testing.T) {
	sock := fakeDaemon(t)
	c, started, root := startAs(t, sock, api.RoleEditor)

	uri := fileURI(filepath.Join(root, "a.go"))
	c.SetOpenBuffer(uri, "package a // live")
	c.SetDiagnostics(uri, []api.Diagnostic{{Severity: api.SeverityError, Message: "boom"}})

	// file.read is editor-aware: a bound editor serves the live buffer, so the
	// result is the editor's content with Open=true (not a disk read).
	var rd api.FileReadResult
	mustCall(t, c, "file.read", api.FileReadParams{SessionID: started.SessionID, URI: uri}, &rd)
	if !rd.Open || rd.Content != "package a // live" {
		t.Fatalf("file.read = %+v, want the bound editor's live buffer", rd)
	}

	// lsp.diagnostics routes to the bound editor and returns its diagnostics.
	var ld api.LSPDiagnosticsResult
	mustCall(t, c, "lsp.diagnostics", api.LSPDiagnosticsParams{SessionID: started.SessionID, URI: uri}, &ld)
	if !ld.Open || len(ld.Diagnostics) != 1 || ld.Diagnostics[0].Message != "boom" {
		t.Fatalf("lsp.diagnostics = %+v, want the bound editor's diagnostics", ld)
	}
}

// TestObserverClientLeavesSessionHeadless proves a non-editor caller does not
// bind: editor-local reverse calls have no owner to route to.
func TestObserverClientLeavesSessionHeadless(t *testing.T) {
	sock := fakeDaemon(t)
	c, started, root := startAs(t, sock, api.RoleObserver)

	// lsp.diagnostics needs the bound editor; headless -> editor_unavailable.
	cx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := c.Call(cx, "lsp.diagnostics", api.LSPDiagnosticsParams{SessionID: started.SessionID, URI: fileURI(filepath.Join(root, "a.go"))}, &api.LSPDiagnosticsResult{})
	var rpcErr *rpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != api.ErrEditorUnavailable {
		t.Fatalf("headless lsp.diagnostics err = %v, want editor_unavailable", err)
	}
}
