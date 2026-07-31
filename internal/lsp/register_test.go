package lsp

import (
	"context"
	"errors"
	"testing"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/dispatch"
	"github.com/dusto/tend/internal/editor"
	"github.com/dusto/tend/internal/rpc"
	"github.com/dusto/tend/internal/session"
)

// dispatchDiag registers the LSP methods over a session "s1" rooted at root and
// dispatches one lsp.diagnostics request with the given params JSON, returning
// the raw rpc error (nil on success).
func dispatchDiag(t *testing.T, ed editorClient, root, paramsJSON string) error {
	t.Helper()
	r := session.NewRegistry()
	r.Create("s1", "codex", "ws1", api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"}, root)
	mux := dispatch.NewMux(api.PluginToDaemon)
	if err := Register(mux, NewService(r, ed, nil, Options{})); err != nil {
		t.Fatalf("Register: %v", err)
	}
	_, err := mux.Handle(context.Background(), &rpc.Request{
		Method: MethodDiagnostics,
		Params: []byte(paramsJSON),
	})
	return err
}

func TestRegisterHeadlessMapsToEditorUnavailable(t *testing.T) {
	err := dispatchDiag(t, &fakeEditor{currentErr: editor.ErrEditorUnavailable}, t.TempDir(), `{"session_id":"s1"}`)
	var rerr *rpc.Error
	if !errors.As(err, &rerr) || rerr.Code != api.ErrEditorUnavailable {
		t.Fatalf("err = %v, want rpc error code %d", err, api.ErrEditorUnavailable)
	}
}

func TestRegisterUnknownSessionMapsToInvalidParams(t *testing.T) {
	err := dispatchDiag(t, &fakeEditor{}, t.TempDir(), `{"session_id":"nope"}`)
	var rerr *rpc.Error
	if !errors.As(err, &rerr) || rerr.Code != rpc.CodeInvalidParams {
		t.Fatalf("err = %v, want invalid params", err)
	}
}

func TestRegisterOutsideWorktreeMapsToInvalidParams(t *testing.T) {
	err := dispatchDiag(t, &fakeEditor{}, t.TempDir(), `{"session_id":"s1","uri":"file:///etc/passwd"}`)
	var rerr *rpc.Error
	if !errors.As(err, &rerr) || rerr.Code != rpc.CodeInvalidParams {
		t.Fatalf("err = %v, want invalid params for an out-of-worktree uri", err)
	}
}

func TestRegisterSuccess(t *testing.T) {
	root := t.TempDir()
	ed := &fakeEditor{open: true, current: api.EditorCurrentBufferResult{URI: fileURI(root + "/a.go")}}
	if err := dispatchDiag(t, ed, root, `{"session_id":"s1"}`); err != nil {
		t.Fatalf("err = %v, want success", err)
	}
}
