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
	calls []string
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
	sessions.Create("s1", "codex", api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"}, "/repo")
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

func TestServiceHeadlessSessionUnavailable(t *testing.T) {
	sessions := session.NewRegistry()
	sessions.Create("s1", "codex", api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"}, "/repo")
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
	sessions.Create("s1", "codex", api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"}, "/repo")
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
