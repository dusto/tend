package lsp

import (
	"context"
	"errors"
	"testing"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/dispatch"
	"github.com/dusto/tend/internal/editor"
	"github.com/dusto/tend/internal/rpc"
)

// callDiagnostics registers the LSP methods on a mux and dispatches one
// lsp.diagnostics request, returning the raw rpc error (nil on success).
func callDiagnostics(t *testing.T, ed editorClient) error {
	t.Helper()
	mux := dispatch.NewMux(api.PluginToDaemon)
	if err := Register(mux, NewService(ed)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	_, err := mux.Handle(context.Background(), &rpc.Request{
		Method: MethodDiagnostics,
		Params: []byte(`{"session_id":"s1"}`),
	})
	return err
}

func TestRegisterHeadlessMapsToEditorUnavailable(t *testing.T) {
	err := callDiagnostics(t, &fakeEditor{currentErr: editor.ErrEditorUnavailable})
	var rerr *rpc.Error
	if !errors.As(err, &rerr) || rerr.Code != api.ErrEditorUnavailable {
		t.Fatalf("err = %v, want rpc error code %d", err, api.ErrEditorUnavailable)
	}
}

func TestRegisterUnknownSessionMapsToInvalidParams(t *testing.T) {
	err := callDiagnostics(t, &fakeEditor{currentErr: editor.ErrNoSession})
	var rerr *rpc.Error
	if !errors.As(err, &rerr) || rerr.Code != rpc.CodeInvalidParams {
		t.Fatalf("err = %v, want invalid params", err)
	}
}

func TestRegisterSuccess(t *testing.T) {
	err := callDiagnostics(t, &fakeEditor{
		current: api.EditorCurrentBufferResult{URI: "file:///repo/a.go"},
		diag:    api.EditorDiagnosticsResult{URI: "file:///repo/a.go", Open: true},
	})
	if err != nil {
		t.Fatalf("err = %v, want success", err)
	}
}
