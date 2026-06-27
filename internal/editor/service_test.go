package editor

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/client"
	"github.com/dusto/tend/internal/rpc"
	"github.com/dusto/tend/internal/session"
)

// fakeEditor answers the daemon->editor reverse calls over a pipe, recording the
// methods it was asked for.
type fakeEditor struct {
	calls   []string
	opened  []string
	diffed  *api.EditorDiffParams
	diagURI string
}

func (e *fakeEditor) handle(_ context.Context, req *rpc.Request) (any, error) {
	e.calls = append(e.calls, req.Method)
	switch req.Method {
	case MethodCurrentBuffer:
		return api.EditorCurrentBufferResult{URI: "file:///repo/a.go"}, nil
	case MethodReadBuffer:
		tick := int64(7)
		return api.EditorReadBufferResult{Content: "package a\n", Base: api.FileBase{ChangedTick: &tick}, Open: true}, nil
	case MethodWriteBuffer:
		var p api.EditorWriteBufferParams
		_ = json.Unmarshal(req.Params, &p)
		tick := int64(8)
		return api.EditorWriteBufferResult{Base: api.FileBase{ChangedTick: &tick}}, nil
	case MethodSelection:
		return api.EditorSelectionResult{
			URI:   "file:///repo/a.go",
			Range: api.Range{Start: api.Position{Line: 1, ByteCol: 0}, End: api.Position{Line: 1, ByteCol: 4}},
		}, nil
	case MethodOpen:
		var p api.EditorOpenParams
		_ = json.Unmarshal(req.Params, &p)
		e.opened = p.URIs
		return api.EditorOpenResult{}, nil
	case MethodDiff:
		var p api.EditorDiffParams
		_ = json.Unmarshal(req.Params, &p)
		e.diffed = &p
		return api.EditorDiffResult{}, nil
	case MethodDiagnostics:
		var p api.EditorDiagnosticsParams
		_ = json.Unmarshal(req.Params, &p)
		e.diagURI = p.URI
		return api.EditorDiagnosticsResult{
			URI:         p.URI,
			Open:        true,
			Diagnostics: []api.Diagnostic{{Severity: api.SeverityError, Message: "boom"}},
		}, nil
	}
	return nil, &rpc.Error{Code: rpc.CodeMethodNotFound, Message: req.Method}
}

// boundFixture wires a session bound to an editor whose connection is a live
// pipe to a fakeEditor, and returns the Service plus the fake.
func boundFixture(t *testing.T) (*Service, *fakeEditor) {
	t.Helper()
	editor := &fakeEditor{}
	a, b := net.Pipe()
	editorConn := rpc.NewConn(a, rpc.HandlerFunc(editor.handle))
	daemonSide := rpc.NewConn(b, nil)
	t.Cleanup(func() { _ = editorConn.Close(); _ = daemonSide.Close() })

	sessions := session.NewRegistry()
	sessions.Create("s1", "codex", "ws1", api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"}, "/repo")
	clients := client.NewRegistry()
	clients.Register("ed1", client.Capabilities{Role: api.RoleEditor}, daemonSide)

	binder := NewBinder(sessions, clients)
	if err := binder.Claim("s1", "ed1"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	return NewService(binder, clients), editor
}

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestServiceRoutesToBoundEditor(t *testing.T) {
	svc, editor := boundFixture(t)

	cur, err := svc.CurrentBuffer(ctx(t), "s1")
	if err != nil || cur.URI != "file:///repo/a.go" {
		t.Fatalf("CurrentBuffer = %+v, err = %v", cur, err)
	}

	rb, err := svc.ReadBuffer(ctx(t), "s1", api.EditorReadBufferParams{URI: "file:///repo/a.go"})
	if err != nil || rb.Content != "package a\n" || !rb.Open || rb.Base.ChangedTick == nil || *rb.Base.ChangedTick != 7 {
		t.Fatalf("ReadBuffer = %+v, err = %v", rb, err)
	}

	wb, err := svc.WriteBuffer(ctx(t), "s1", api.EditorWriteBufferParams{URI: "file:///repo/a.go", Content: "package a // edited\n"})
	if err != nil || wb.Base.ChangedTick == nil || *wb.Base.ChangedTick != 8 {
		t.Fatalf("WriteBuffer = %+v, err = %v", wb, err)
	}

	sel, err := svc.Selection(ctx(t), "s1")
	if err != nil || sel.Range.End.ByteCol != 4 {
		t.Fatalf("Selection = %+v, err = %v", sel, err)
	}

	want := []string{MethodCurrentBuffer, MethodReadBuffer, MethodWriteBuffer, MethodSelection}
	if len(editor.calls) != len(want) {
		t.Fatalf("editor calls = %v, want %v", editor.calls, want)
	}
}

func TestServiceOpenAndDiffRouteToBoundEditor(t *testing.T) {
	svc, editor := boundFixture(t)

	if _, err := svc.Open(ctx(t), "s1", api.EditorOpenParams{
		URIs: []string{"file:///repo/a.go", "file:///repo/b.go"},
	}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(editor.opened) != 2 || editor.opened[1] != "file:///repo/b.go" {
		t.Fatalf("opened = %v", editor.opened)
	}

	if _, err := svc.Diff(ctx(t), "s1", api.EditorDiffParams{
		ChangeSetID: "cs1",
		Files: []api.EditorDiffFile{
			{URI: "file:///repo/a.go", Before: "old\n", After: "new\n"},
		},
	}); err != nil {
		t.Fatalf("Diff: %v", err)
	}
	d := editor.diffed
	if d == nil || d.ChangeSetID != "cs1" || len(d.Files) != 1 {
		t.Fatalf("diffed = %+v", d)
	}
	// The snapshots travel in the request: the editor diffs the named set,
	// never an undefined current state.
	if d.Files[0].Before != "old\n" || d.Files[0].After != "new\n" {
		t.Errorf("diff files = %+v", d.Files)
	}

	want := []string{MethodOpen, MethodDiff}
	if len(editor.calls) != len(want) || editor.calls[0] != want[0] || editor.calls[1] != want[1] {
		t.Errorf("editor calls = %v, want %v", editor.calls, want)
	}
}

func TestServiceDiagnosticsRoutesToBoundEditor(t *testing.T) {
	svc, editor := boundFixture(t)

	res, err := svc.Diagnostics(ctx(t), "s1", api.EditorDiagnosticsParams{URI: "file:///repo/a.go"})
	if err != nil {
		t.Fatalf("Diagnostics: %v", err)
	}
	if res.URI != "file:///repo/a.go" || !res.Open || len(res.Diagnostics) != 1 {
		t.Fatalf("result = %+v", res)
	}
	if editor.diagURI != "file:///repo/a.go" {
		t.Errorf("editor queried uri = %q", editor.diagURI)
	}
	if len(editor.calls) != 1 || editor.calls[0] != MethodDiagnostics {
		t.Errorf("editor calls = %v", editor.calls)
	}
}

func TestServiceDiagnosticsHeadlessUnavailable(t *testing.T) {
	sessions := session.NewRegistry()
	sessions.Create("s1", "codex", "ws1", api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"}, "/repo")
	clients := client.NewRegistry()
	svc := NewService(NewBinder(sessions, clients), clients)

	if _, err := svc.Diagnostics(ctx(t), "s1", api.EditorDiagnosticsParams{URI: "file:///x"}); !errors.Is(err, ErrEditorUnavailable) {
		t.Errorf("headless Diagnostics err = %v, want ErrEditorUnavailable", err)
	}
}

func TestServiceOpenHeadlessUnavailable(t *testing.T) {
	sessions := session.NewRegistry()
	sessions.Create("s1", "codex", "ws1", api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"}, "/repo")
	clients := client.NewRegistry()
	svc := NewService(NewBinder(sessions, clients), clients)

	if _, err := svc.Open(ctx(t), "s1", api.EditorOpenParams{URIs: []string{"file:///x"}}); !errors.Is(err, ErrEditorUnavailable) {
		t.Errorf("headless Open err = %v, want ErrEditorUnavailable", err)
	}
	if _, err := svc.Diff(ctx(t), "s1", api.EditorDiffParams{}); !errors.Is(err, ErrEditorUnavailable) {
		t.Errorf("headless Diff err = %v, want ErrEditorUnavailable", err)
	}
}

func TestServiceHeadlessSessionUnavailable(t *testing.T) {
	sessions := session.NewRegistry()
	sessions.Create("s1", "codex", "ws1", api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"}, "/repo")
	clients := client.NewRegistry()
	svc := NewService(NewBinder(sessions, clients), clients)

	if _, err := svc.ReadBuffer(ctx(t), "s1", api.EditorReadBufferParams{}); !errors.Is(err, ErrEditorUnavailable) {
		t.Errorf("headless ReadBuffer err = %v, want ErrEditorUnavailable", err)
	}
	if _, err := svc.CurrentBuffer(ctx(t), "missing"); !errors.Is(err, ErrNoSession) {
		t.Errorf("unknown-session err = %v, want ErrNoSession", err)
	}
}

func TestServiceOwnerReRegisteredAsObserverUnavailable(t *testing.T) {
	svc, editor := boundFixture(t)
	// The bound editor id reconnects/re-registers as an observer; it must no
	// longer receive editor-local reverse calls even though it still has a caller.
	prev, _ := svc.clients.Get("ed1")
	svc.clients.Register("ed1", client.Capabilities{Role: api.RoleObserver}, prev.Caller)

	if _, err := svc.CurrentBuffer(ctx(t), "s1"); !errors.Is(err, ErrEditorUnavailable) {
		t.Errorf("observer re-register err = %v, want ErrEditorUnavailable", err)
	}
	if len(editor.calls) != 0 {
		t.Errorf("editor received calls after demotion: %v", editor.calls)
	}
}

func TestServiceOwnerWithoutCallerUnavailable(t *testing.T) {
	sessions := session.NewRegistry()
	sessions.Create("s1", "codex", "ws1", api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"}, "/repo")
	clients := client.NewRegistry()
	clients.Register("ed1", client.Capabilities{Role: api.RoleEditor}, nil) // no reverse caller
	binder := NewBinder(sessions, clients)
	if err := binder.Claim("s1", "ed1"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	svc := NewService(binder, clients)

	if _, err := svc.Selection(ctx(t), "s1"); !errors.Is(err, ErrEditorUnavailable) {
		t.Errorf("owner-without-caller err = %v, want ErrEditorUnavailable", err)
	}
}
