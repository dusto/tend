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
	symbols    []api.DocumentSymbol
	locations  []api.Location
	hover      string
	actions    []api.CodeAction
	navErr     error
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

func (f *fakeEditor) Symbols(_ context.Context, _ api.SessionID, p api.EditorSymbolsParams) (api.EditorSymbolsResult, error) {
	f.queriedURI = p.URI
	if f.navErr != nil {
		return api.EditorSymbolsResult{}, f.navErr
	}
	return api.EditorSymbolsResult{URI: p.URI, Open: f.open, Symbols: f.symbols}, nil
}

func (f *fakeEditor) Definition(_ context.Context, _ api.SessionID, p api.EditorDefinitionParams) (api.EditorDefinitionResult, error) {
	f.queriedURI = p.URI
	if f.navErr != nil {
		return api.EditorDefinitionResult{}, f.navErr
	}
	return api.EditorDefinitionResult{URI: p.URI, Open: f.open, Locations: f.locations}, nil
}

func (f *fakeEditor) References(_ context.Context, _ api.SessionID, p api.EditorReferencesParams) (api.EditorReferencesResult, error) {
	f.queriedURI = p.URI
	if f.navErr != nil {
		return api.EditorReferencesResult{}, f.navErr
	}
	return api.EditorReferencesResult{URI: p.URI, Open: f.open, Locations: f.locations}, nil
}

func (f *fakeEditor) Hover(_ context.Context, _ api.SessionID, p api.EditorHoverParams) (api.EditorHoverResult, error) {
	f.queriedURI = p.URI
	if f.navErr != nil {
		return api.EditorHoverResult{}, f.navErr
	}
	return api.EditorHoverResult{URI: p.URI, Open: f.open, Contents: f.hover}, nil
}

func (f *fakeEditor) CodeActions(_ context.Context, _ api.SessionID, p api.EditorCodeActionsParams) (api.EditorCodeActionsResult, error) {
	f.queriedURI = p.URI
	if f.navErr != nil {
		return api.EditorCodeActionsResult{}, f.navErr
	}
	return api.EditorCodeActionsResult{URI: p.URI, Open: f.open, Actions: f.actions}, nil
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
	r.Create("s1", "codex", "ws1", api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"}, root)
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

func TestSymbolsEditorFresh(t *testing.T) {
	ed := &fakeEditor{open: true, symbols: []api.DocumentSymbol{
		{Name: "Server", Kind: "struct"},
		{Name: "Serve", Kind: "method", ContainerName: "Server"},
	}}
	svc, root := newSvc(t, ed)
	uri := fileURI(filepath.Join(root, "a.go"))

	res, err := svc.Symbols(context.Background(), api.LSPSymbolsParams{SessionID: "s1", URI: uri})
	if err != nil {
		t.Fatalf("Symbols: %v", err)
	}
	if res.URI != uri || !res.Open || len(res.Symbols) != 2 || res.Symbols[1].ContainerName != "Server" {
		t.Fatalf("result = %+v", res)
	}
	if ed.queriedURI != uri {
		t.Errorf("queried uri = %q, want requested", ed.queriedURI)
	}
}

func TestSymbolsEmptyURIResolvesCurrentBuffer(t *testing.T) {
	ed := &fakeEditor{open: true}
	svc, root := newSvc(t, ed)
	cur := fileURI(filepath.Join(root, "cur.go"))
	ed.current = api.EditorCurrentBufferResult{URI: cur}

	if _, err := svc.Symbols(context.Background(), api.LSPSymbolsParams{SessionID: "s1"}); err != nil {
		t.Fatalf("Symbols: %v", err)
	}
	if ed.queriedURI != cur {
		t.Errorf("queried uri = %q, want current buffer", ed.queriedURI)
	}
}

func TestSymbolsResultIsNonNilSlice(t *testing.T) {
	svc, root := newSvc(t, &fakeEditor{open: true})
	res, err := svc.Symbols(context.Background(), api.LSPSymbolsParams{
		SessionID: "s1", URI: fileURI(filepath.Join(root, "a.go")),
	})
	if err != nil {
		t.Fatalf("Symbols: %v", err)
	}
	if res.Symbols == nil {
		t.Error("Symbols should be a non-nil slice so it marshals as []")
	}
}

func TestDefinitionReturnsLocationsIncludingOutsideWorktree(t *testing.T) {
	// A definition may resolve into a dependency/stdlib outside the worktree; the
	// input file is bounded, but result locations are read-only metadata and are
	// returned as-is (not filtered or refused).
	dep := "file:///usr/lib/go/src/fmt/print.go"
	ed := &fakeEditor{open: true, locations: []api.Location{{URI: dep}}}
	svc, root := newSvc(t, ed)

	res, err := svc.Definition(context.Background(), api.LSPDefinitionParams{
		SessionID: "s1", URI: fileURI(filepath.Join(root, "a.go")),
		Position: api.Position{Line: 3, ByteCol: 5},
	})
	if err != nil {
		t.Fatalf("Definition: %v", err)
	}
	if len(res.Locations) != 1 || res.Locations[0].URI != dep {
		t.Fatalf("locations = %+v, want the out-of-worktree definition", res.Locations)
	}
}

func TestReferencesForwardsIncludeDeclaration(t *testing.T) {
	ed := &fakeEditor{open: true, locations: []api.Location{{URI: "file:///x"}}}
	svc, root := newSvc(t, ed)

	res, err := svc.References(context.Background(), api.LSPReferencesParams{
		SessionID: "s1", URI: fileURI(filepath.Join(root, "a.go")),
		Position: api.Position{Line: 1}, IncludeDeclaration: true,
	})
	if err != nil {
		t.Fatalf("References: %v", err)
	}
	if len(res.Locations) != 1 {
		t.Errorf("locations = %+v, want 1", res.Locations)
	}
}

func TestHoverReturnsContents(t *testing.T) {
	ed := &fakeEditor{open: true, hover: "func Serve()"}
	svc, root := newSvc(t, ed)

	res, err := svc.Hover(context.Background(), api.LSPHoverParams{
		SessionID: "s1", URI: fileURI(filepath.Join(root, "a.go")),
		Position: api.Position{Line: 2, ByteCol: 1},
	})
	if err != nil {
		t.Fatalf("Hover: %v", err)
	}
	if res.Contents != "func Serve()" || !res.Open {
		t.Errorf("result = %+v", res)
	}
}

// The navigation methods share the worktree-boundary path with Diagnostics: an
// explicitly named out-of-worktree uri is refused before the editor is queried,
// and a headless session surfaces editor_unavailable.
func TestNavigationOutsideWorktreeRejected(t *testing.T) {
	ed := &fakeEditor{open: true}
	svc, _ := newSvc(t, ed)

	if _, err := svc.Symbols(context.Background(), api.LSPSymbolsParams{
		SessionID: "s1", URI: "file:///etc/passwd",
	}); !errors.Is(err, worktree.ErrOutsideWorkspace) {
		t.Errorf("symbols err = %v, want ErrOutsideWorkspace", err)
	}
	if _, err := svc.Definition(context.Background(), api.LSPDefinitionParams{
		SessionID: "s1", URI: "file:///etc/passwd",
	}); !errors.Is(err, worktree.ErrOutsideWorkspace) {
		t.Errorf("definition err = %v, want ErrOutsideWorkspace", err)
	}
	if ed.queriedURI != "" {
		t.Errorf("editor queried for an out-of-worktree uri: %q", ed.queriedURI)
	}
}

func TestNavigationHeadlessUnavailable(t *testing.T) {
	ed := &fakeEditor{currentErr: editor.ErrEditorUnavailable, navErr: editor.ErrEditorUnavailable}
	svc, root := newSvc(t, ed)
	uri := fileURI(filepath.Join(root, "a.go"))

	if _, err := svc.Hover(context.Background(), api.LSPHoverParams{SessionID: "s1", URI: uri}); !errors.Is(err, editor.ErrEditorUnavailable) {
		t.Errorf("hover headless err = %v, want ErrEditorUnavailable", err)
	}
	if _, err := svc.Symbols(context.Background(), api.LSPSymbolsParams{SessionID: "s1"}); !errors.Is(err, editor.ErrEditorUnavailable) {
		t.Errorf("symbols empty-uri headless err = %v, want ErrEditorUnavailable", err)
	}
}

func TestCodeActionsListsWithChangeSetReadyEdits(t *testing.T) {
	// An edit-carrying action arrives with change-set-ready targets (URI + Base +
	// edits), so the caller can hand its Changes straight to file.apply_change_set.
	tick := int64(7)
	ed := &fakeEditor{open: true, actions: []api.CodeAction{
		{
			Title: "Organize Imports", Kind: "source.organizeImports", Edit: true,
			Changes: []api.FileChange{{
				URI:  "file:///repo/a.go",
				Base: api.FileBase{ChangedTick: &tick},
				Kind: api.FileChangePatch,
				Edits: []api.TextEdit{{
					Range: api.Range{Start: api.Position{Line: 0}, End: api.Position{Line: 1}}, NewText: "",
				}},
			}},
		},
		// A command-only action is listed for visibility but is not applyable.
		{Title: "Run generator", Kind: "source", Edit: false},
	}}
	svc, root := newSvc(t, ed)
	uri := fileURI(filepath.Join(root, "a.go"))

	res, err := svc.CodeActions(context.Background(), api.LSPCodeActionsParams{
		SessionID: "s1", URI: uri, Range: api.Range{Start: api.Position{Line: 0}, End: api.Position{Line: 5}},
	})
	if err != nil {
		t.Fatalf("CodeActions: %v", err)
	}
	if res.URI != uri || !res.Open || len(res.Actions) != 2 {
		t.Fatalf("result = %+v", res)
	}
	edit := res.Actions[0]
	if !edit.Edit || len(edit.Changes) != 1 || edit.Changes[0].Base.ChangedTick == nil {
		t.Errorf("edit action = %+v, want change-set-ready with a base", edit)
	}
	if res.Actions[1].Edit || len(res.Actions[1].Changes) != 0 {
		t.Errorf("command action = %+v, want no applyable changes", res.Actions[1])
	}
}

func TestCodeActionsResultIsNonNilSlice(t *testing.T) {
	svc, root := newSvc(t, &fakeEditor{open: true})
	res, err := svc.CodeActions(context.Background(), api.LSPCodeActionsParams{
		SessionID: "s1", URI: fileURI(filepath.Join(root, "a.go")),
	})
	if err != nil {
		t.Fatalf("CodeActions: %v", err)
	}
	if res.Actions == nil {
		t.Error("Actions should be a non-nil slice so it marshals as []")
	}
}

func TestCodeActionsOutsideWorktreeRejected(t *testing.T) {
	ed := &fakeEditor{open: true}
	svc, _ := newSvc(t, ed)
	if _, err := svc.CodeActions(context.Background(), api.LSPCodeActionsParams{
		SessionID: "s1", URI: "file:///etc/passwd",
	}); !errors.Is(err, worktree.ErrOutsideWorkspace) {
		t.Errorf("err = %v, want ErrOutsideWorkspace", err)
	}
	if ed.queriedURI != "" {
		t.Errorf("editor queried for an out-of-worktree uri: %q", ed.queriedURI)
	}
}

func TestNavigationUnknownSession(t *testing.T) {
	svc, _ := newSvc(t, &fakeEditor{})
	if _, err := svc.Symbols(context.Background(), api.LSPSymbolsParams{SessionID: "nope"}); !errors.Is(err, ErrNoSession) {
		t.Errorf("symbols err = %v, want ErrNoSession", err)
	}
	if _, err := svc.Hover(context.Background(), api.LSPHoverParams{SessionID: "nope"}); !errors.Is(err, ErrNoSession) {
		t.Errorf("hover err = %v, want ErrNoSession", err)
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
