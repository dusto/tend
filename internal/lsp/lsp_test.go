package lsp

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/editor"
	"github.com/dusto/tend/internal/session"
	"github.com/dusto/tend/internal/worktree"
)

// fakeEditor stands in for editor.Service: it returns a canned current buffer
// and per-buffer diagnostics (or an error, e.g. editor.ErrEditorUnavailable for
// a headless session), echoing the queried uri into the result and recording it.
type fakeEditor struct {
	current    api.EditorCurrentBufferResult
	currentErr error
	open       bool
	diags      []api.Diagnostic
	diagErr    error
	queriedURI string
}

func (f *fakeEditor) CurrentBuffer(context.Context, api.SessionID) (api.EditorCurrentBufferResult, error) {
	return f.current, f.currentErr
}

func (f *fakeEditor) Diagnostics(_ context.Context, _ api.SessionID, p api.EditorDiagnosticsParams) (api.EditorDiagnosticsResult, error) {
	f.queriedURI = p.URI
	if f.diagErr != nil {
		return api.EditorDiagnosticsResult{}, f.diagErr
	}
	return api.EditorDiagnosticsResult{URI: p.URI, Open: f.open, Diagnostics: f.diags}, nil
}

func diag(sev api.DiagnosticSeverity, msg string) api.Diagnostic {
	return api.Diagnostic{Severity: sev, Message: msg}
}

func fileURI(path string) string {
	return (&url.URL{Scheme: "file", Path: path}).String()
}

// newSvc builds an LSP service over a session rooted at a fresh temp worktree,
// returning the service and the worktree root.
func newSvc(t *testing.T, ed editorClient) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	r := session.NewRegistry()
	r.Create("s1", "codex", api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"}, root)
	return NewService(r, ed), root
}

func TestDiagnosticsEditorFresh(t *testing.T) {
	ed := &fakeEditor{open: true, diags: []api.Diagnostic{
		diag(api.SeverityError, "boom"),
		diag(api.SeverityWarning, "careful"),
	}}
	svc, root := newSvc(t, ed)
	uri := fileURI(filepath.Join(root, "a.go"))

	res, err := svc.Diagnostics(context.Background(), api.LSPDiagnosticsParams{SessionID: "s1", URI: uri})
	if err != nil {
		t.Fatalf("Diagnostics: %v", err)
	}
	if res.URI != uri || !res.Open || len(res.Diagnostics) != 2 {
		t.Fatalf("result = %+v", res)
	}
	if ed.queriedURI != uri {
		t.Errorf("queried uri = %q, want the requested uri", ed.queriedURI)
	}
}

func TestDiagnosticsSeverityFilterIsMinimum(t *testing.T) {
	ed := &fakeEditor{open: true, diags: []api.Diagnostic{
		diag(api.SeverityError, "e"),
		diag(api.SeverityWarning, "w"),
		diag(api.SeverityInfo, "i"),
		diag(api.SeverityHint, "h"),
	}}
	svc, root := newSvc(t, ed)

	res, err := svc.Diagnostics(context.Background(), api.LSPDiagnosticsParams{
		SessionID: "s1", URI: fileURI(filepath.Join(root, "a.go")), Severity: api.SeverityWarning,
	})
	if err != nil {
		t.Fatalf("Diagnostics: %v", err)
	}
	if len(res.Diagnostics) != 2 {
		t.Fatalf("filtered = %+v, want error+warning only", res.Diagnostics)
	}
	for _, d := range res.Diagnostics {
		if d.Severity == api.SeverityInfo || d.Severity == api.SeverityHint {
			t.Errorf("severity %q should have been filtered out", d.Severity)
		}
	}
}

func TestDiagnosticsEmptyURIResolvesCurrentBuffer(t *testing.T) {
	ed := &fakeEditor{open: true}
	svc, root := newSvc(t, ed)
	cur := fileURI(filepath.Join(root, "cur.go"))
	ed.current = api.EditorCurrentBufferResult{URI: cur}

	if _, err := svc.Diagnostics(context.Background(), api.LSPDiagnosticsParams{SessionID: "s1"}); err != nil {
		t.Fatalf("Diagnostics: %v", err)
	}
	if ed.queriedURI != cur {
		t.Errorf("queried uri = %q, want the current buffer's uri", ed.queriedURI)
	}
}

func TestDiagnosticsNoCurrentBufferIsEmpty(t *testing.T) {
	ed := &fakeEditor{current: api.EditorCurrentBufferResult{URI: ""}}
	svc, _ := newSvc(t, ed)

	res, err := svc.Diagnostics(context.Background(), api.LSPDiagnosticsParams{SessionID: "s1"})
	if err != nil {
		t.Fatalf("Diagnostics: %v", err)
	}
	if len(res.Diagnostics) != 0 || res.Open {
		t.Errorf("result = %+v, want empty for no active file buffer", res)
	}
	if ed.queriedURI != "" {
		t.Errorf("should not query diagnostics when there is no current buffer (got %q)", ed.queriedURI)
	}
}

func TestDiagnosticsClosedFileNotOpen(t *testing.T) {
	// The editor has no live buffer for the file: milestone-0 has no daemon
	// index, so the result is simply not-open with no diagnostics.
	ed := &fakeEditor{open: false}
	svc, root := newSvc(t, ed)

	res, err := svc.Diagnostics(context.Background(), api.LSPDiagnosticsParams{
		SessionID: "s1", URI: fileURI(filepath.Join(root, "closed.go")),
	})
	if err != nil {
		t.Fatalf("Diagnostics: %v", err)
	}
	if res.Open || len(res.Diagnostics) != 0 {
		t.Errorf("result = %+v, want not-open empty", res)
	}
}

func TestDiagnosticsUnknownSession(t *testing.T) {
	svc, _ := newSvc(t, &fakeEditor{})
	if _, err := svc.Diagnostics(context.Background(), api.LSPDiagnosticsParams{SessionID: "nope"}); !errors.Is(err, ErrNoSession) {
		t.Errorf("err = %v, want ErrNoSession", err)
	}
}

func TestDiagnosticsHeadlessUnavailable(t *testing.T) {
	// A headless session: the editor service returns ErrEditorUnavailable both
	// for the current-buffer resolve and a direct query.
	ed := &fakeEditor{currentErr: editor.ErrEditorUnavailable, diagErr: editor.ErrEditorUnavailable}
	svc, root := newSvc(t, ed)

	if _, err := svc.Diagnostics(context.Background(), api.LSPDiagnosticsParams{SessionID: "s1"}); !errors.Is(err, editor.ErrEditorUnavailable) {
		t.Errorf("empty-uri headless err = %v, want ErrEditorUnavailable", err)
	}
	if _, err := svc.Diagnostics(context.Background(), api.LSPDiagnosticsParams{
		SessionID: "s1", URI: fileURI(filepath.Join(root, "a.go")),
	}); !errors.Is(err, editor.ErrEditorUnavailable) {
		t.Errorf("uri headless err = %v, want ErrEditorUnavailable", err)
	}
}

func TestDiagnosticsOutsideWorktreeRejected(t *testing.T) {
	ed := &fakeEditor{open: true}
	svc, _ := newSvc(t, ed)

	// An absolute path outside the session worktree must be refused before the
	// editor is queried — an agent for one repo cannot read another's files.
	if _, err := svc.Diagnostics(context.Background(), api.LSPDiagnosticsParams{
		SessionID: "s1", URI: "file:///etc/passwd",
	}); !errors.Is(err, worktree.ErrOutsideWorkspace) {
		t.Errorf("err = %v, want ErrOutsideWorkspace", err)
	}
	if ed.queriedURI != "" {
		t.Errorf("editor was queried for an out-of-worktree uri: %q", ed.queriedURI)
	}

	if _, err := svc.Diagnostics(context.Background(), api.LSPDiagnosticsParams{
		SessionID: "s1", URI: "http://example.com/a",
	}); !errors.Is(err, worktree.ErrBadURI) {
		t.Errorf("err = %v, want ErrBadURI for a non-file uri", err)
	}
}

func TestDiagnosticsSymlinkEscapeRejected(t *testing.T) {
	ed := &fakeEditor{open: true}
	svc, root := newSvc(t, ed)

	// A secret outside the worktree, reachable through a symlinked dir inside it.
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("s"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if _, err := svc.Diagnostics(context.Background(), api.LSPDiagnosticsParams{
		SessionID: "s1", URI: fileURI(filepath.Join(root, "link", "secret.txt")),
	}); !errors.Is(err, worktree.ErrOutsideWorkspace) {
		t.Errorf("through symlink: err = %v, want ErrOutsideWorkspace", err)
	}
	if ed.queriedURI != "" {
		t.Errorf("editor was queried through a symlink escape: %q", ed.queriedURI)
	}
}

func TestDiagnosticsCurrentBufferOutsideWorktreeIsEmpty(t *testing.T) {
	// The user's focused buffer is an unrelated file outside this session's
	// worktree. The agent named nothing, so this is not a violation — but the
	// out-of-scope buffer must not be diagnosed or its path leaked.
	ed := &fakeEditor{open: true, current: api.EditorCurrentBufferResult{URI: "file:///etc/hosts"}}
	svc, _ := newSvc(t, ed)

	res, err := svc.Diagnostics(context.Background(), api.LSPDiagnosticsParams{SessionID: "s1"})
	if err != nil {
		t.Fatalf("Diagnostics: %v", err)
	}
	if res.URI != "" || res.Open || len(res.Diagnostics) != 0 {
		t.Errorf("result = %+v, want empty for an out-of-worktree current buffer", res)
	}
	if ed.queriedURI != "" {
		t.Errorf("editor was queried for an out-of-worktree current buffer: %q", ed.queriedURI)
	}
}

func TestDiagnosticsResultIsNonNilSlice(t *testing.T) {
	ed := &fakeEditor{open: true}
	svc, root := newSvc(t, ed)

	res, err := svc.Diagnostics(context.Background(), api.LSPDiagnosticsParams{
		SessionID: "s1", URI: fileURI(filepath.Join(root, "a.go")),
	})
	if err != nil {
		t.Fatalf("Diagnostics: %v", err)
	}
	if res.Diagnostics == nil {
		t.Error("Diagnostics should be a non-nil slice so it marshals as [], not null")
	}
}
