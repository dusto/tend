package lsp

import (
	"context"
	"errors"
	"testing"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/editor"
)

// fakeEditor stands in for editor.Service: it returns canned current-buffer and
// diagnostics results (or an error, e.g. editor.ErrEditorUnavailable for a
// headless session), recording the uri it was queried for.
type fakeEditor struct {
	current    api.EditorCurrentBufferResult
	currentErr error
	diag       api.EditorDiagnosticsResult
	diagErr    error
	queriedURI string
}

func (f *fakeEditor) CurrentBuffer(context.Context, api.SessionID) (api.EditorCurrentBufferResult, error) {
	return f.current, f.currentErr
}

func (f *fakeEditor) Diagnostics(_ context.Context, _ api.SessionID, p api.EditorDiagnosticsParams) (api.EditorDiagnosticsResult, error) {
	f.queriedURI = p.URI
	return f.diag, f.diagErr
}

func diag(sev api.DiagnosticSeverity, msg string) api.Diagnostic {
	return api.Diagnostic{Severity: sev, Message: msg}
}

func TestDiagnosticsEditorFresh(t *testing.T) {
	ed := &fakeEditor{diag: api.EditorDiagnosticsResult{
		URI:  "file:///repo/a.go",
		Open: true,
		Diagnostics: []api.Diagnostic{
			diag(api.SeverityError, "boom"),
			diag(api.SeverityWarning, "careful"),
		},
	}}
	svc := NewService(ed)

	res, err := svc.Diagnostics(context.Background(), api.LSPDiagnosticsParams{
		SessionID: "s1", URI: "file:///repo/a.go",
	})
	if err != nil {
		t.Fatalf("Diagnostics: %v", err)
	}
	if res.URI != "file:///repo/a.go" || !res.Open || len(res.Diagnostics) != 2 {
		t.Fatalf("result = %+v", res)
	}
	if ed.queriedURI != "file:///repo/a.go" {
		t.Errorf("queried uri = %q, want the requested uri", ed.queriedURI)
	}
}

func TestDiagnosticsSeverityFilterIsMinimum(t *testing.T) {
	ed := &fakeEditor{diag: api.EditorDiagnosticsResult{
		URI:  "file:///repo/a.go",
		Open: true,
		Diagnostics: []api.Diagnostic{
			diag(api.SeverityError, "e"),
			diag(api.SeverityWarning, "w"),
			diag(api.SeverityInfo, "i"),
			diag(api.SeverityHint, "h"),
		},
	}}
	svc := NewService(ed)

	res, err := svc.Diagnostics(context.Background(), api.LSPDiagnosticsParams{
		SessionID: "s1", URI: "file:///repo/a.go", Severity: api.SeverityWarning,
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
	ed := &fakeEditor{
		current: api.EditorCurrentBufferResult{URI: "file:///repo/cur.go"},
		diag:    api.EditorDiagnosticsResult{URI: "file:///repo/cur.go", Open: true},
	}
	svc := NewService(ed)

	if _, err := svc.Diagnostics(context.Background(), api.LSPDiagnosticsParams{SessionID: "s1"}); err != nil {
		t.Fatalf("Diagnostics: %v", err)
	}
	if ed.queriedURI != "file:///repo/cur.go" {
		t.Errorf("queried uri = %q, want the current buffer's uri", ed.queriedURI)
	}
}

func TestDiagnosticsNoCurrentBufferIsEmpty(t *testing.T) {
	ed := &fakeEditor{current: api.EditorCurrentBufferResult{URI: ""}}
	svc := NewService(ed)

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
	ed := &fakeEditor{diag: api.EditorDiagnosticsResult{URI: "file:///repo/closed.go", Open: false}}
	svc := NewService(ed)

	res, err := svc.Diagnostics(context.Background(), api.LSPDiagnosticsParams{
		SessionID: "s1", URI: "file:///repo/closed.go",
	})
	if err != nil {
		t.Fatalf("Diagnostics: %v", err)
	}
	if res.Open || len(res.Diagnostics) != 0 {
		t.Errorf("result = %+v, want not-open empty", res)
	}
}

func TestDiagnosticsHeadlessUnavailable(t *testing.T) {
	// A headless session: the editor service returns ErrEditorUnavailable both
	// for the current-buffer resolve and a direct query.
	ed := &fakeEditor{currentErr: editor.ErrEditorUnavailable, diagErr: editor.ErrEditorUnavailable}
	svc := NewService(ed)

	if _, err := svc.Diagnostics(context.Background(), api.LSPDiagnosticsParams{SessionID: "s1"}); !errors.Is(err, editor.ErrEditorUnavailable) {
		t.Errorf("empty-uri headless err = %v, want ErrEditorUnavailable", err)
	}
	if _, err := svc.Diagnostics(context.Background(), api.LSPDiagnosticsParams{SessionID: "s1", URI: "file:///x"}); !errors.Is(err, editor.ErrEditorUnavailable) {
		t.Errorf("uri headless err = %v, want ErrEditorUnavailable", err)
	}
}

func TestDiagnosticsResultIsNonNilSlice(t *testing.T) {
	ed := &fakeEditor{diag: api.EditorDiagnosticsResult{URI: "file:///repo/a.go", Open: true}}
	svc := NewService(ed)

	res, err := svc.Diagnostics(context.Background(), api.LSPDiagnosticsParams{SessionID: "s1", URI: "file:///repo/a.go"})
	if err != nil {
		t.Fatalf("Diagnostics: %v", err)
	}
	if res.Diagnostics == nil {
		t.Error("Diagnostics should be a non-nil slice so it marshals as [], not null")
	}
}
