package editor

import (
	"context"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/client"
	"github.com/dusto/tend/internal/rpc"
)

// Editor reverse-RPC method names (daemon -> bound editor).
const (
	MethodCurrentBuffer = "editor.current_buffer"
	MethodReadBuffer    = "editor.read_buffer"
	MethodWriteBuffer   = "editor.write_buffer"
	MethodSelection     = "editor.selection"
	MethodOpen          = "editor.open"
	MethodDiff          = "editor.diff"
)

// Service routes editor-local calls to a session's editor-binding owner. Each
// method resolves the bound editor through the Binder and issues the
// reverse-direction call on that client's connection, so editor-local effects
// land in exactly one known editor. When a session is headless (no owner) it
// returns ErrEditorUnavailable rather than guessing a target.
type Service struct {
	binder  *Binder
	clients *client.Registry
}

// NewService returns a Service that routes through binder and clients.
func NewService(binder *Binder, clients *client.Registry) *Service {
	return &Service{binder: binder, clients: clients}
}

// CurrentBuffer returns the bound editor's active buffer.
func (s *Service) CurrentBuffer(ctx context.Context, sessionID api.SessionID) (api.EditorCurrentBufferResult, error) {
	caller, err := s.editorCaller(sessionID)
	if err != nil {
		return api.EditorCurrentBufferResult{}, err
	}
	var res api.EditorCurrentBufferResult
	err = caller.Call(ctx, MethodCurrentBuffer, api.EditorCurrentBufferParams{}, &res)
	return res, err
}

// ReadBuffer reads a file through the bound editor (editor-aware base).
func (s *Service) ReadBuffer(ctx context.Context, sessionID api.SessionID, p api.EditorReadBufferParams) (api.EditorReadBufferResult, error) {
	caller, err := s.editorCaller(sessionID)
	if err != nil {
		return api.EditorReadBufferResult{}, err
	}
	var res api.EditorReadBufferResult
	err = caller.Call(ctx, MethodReadBuffer, p, &res)
	return res, err
}

// WriteBuffer writes whole-buffer content through the bound editor.
func (s *Service) WriteBuffer(ctx context.Context, sessionID api.SessionID, p api.EditorWriteBufferParams) (api.EditorWriteBufferResult, error) {
	caller, err := s.editorCaller(sessionID)
	if err != nil {
		return api.EditorWriteBufferResult{}, err
	}
	var res api.EditorWriteBufferResult
	err = caller.Call(ctx, MethodWriteBuffer, p, &res)
	return res, err
}

// Open asks the bound editor to open files in buffers for in-place review.
func (s *Service) Open(ctx context.Context, sessionID api.SessionID, p api.EditorOpenParams) (api.EditorOpenResult, error) {
	caller, err := s.editorCaller(sessionID)
	if err != nil {
		return api.EditorOpenResult{}, err
	}
	var res api.EditorOpenResult
	err = caller.Call(ctx, MethodOpen, p, &res)
	return res, err
}

// Diff asks the bound editor to render a change set's captured before/after
// snapshots in its diff view. The content travels in the request, so the view
// shows the named proposal or applied set, never an undefined current state.
func (s *Service) Diff(ctx context.Context, sessionID api.SessionID, p api.EditorDiffParams) (api.EditorDiffResult, error) {
	caller, err := s.editorCaller(sessionID)
	if err != nil {
		return api.EditorDiffResult{}, err
	}
	var res api.EditorDiffResult
	err = caller.Call(ctx, MethodDiff, p, &res)
	return res, err
}

// Selection returns the bound editor's current selection.
func (s *Service) Selection(ctx context.Context, sessionID api.SessionID) (api.EditorSelectionResult, error) {
	caller, err := s.editorCaller(sessionID)
	if err != nil {
		return api.EditorSelectionResult{}, err
	}
	var res api.EditorSelectionResult
	err = caller.Call(ctx, MethodSelection, api.EditorSelectionParams{}, &res)
	return res, err
}

// editorCaller resolves the reverse caller for a session's bound editor. It
// returns ErrEditorUnavailable when the session is headless or the owner's
// current registry entry is no longer an editor-capable client with a reverse
// caller — for example the same client id reconnected as an observer, in which
// case it must not receive editor-local calls.
func (s *Service) editorCaller(sessionID api.SessionID) (rpc.Caller, error) {
	owner, err := s.binder.Owner(sessionID)
	if err != nil {
		return nil, err
	}
	cl, ok := s.clients.Get(owner)
	if !ok || cl.Caller == nil || !cl.IsEditor() {
		return nil, ErrEditorUnavailable
	}
	return cl.Caller, nil
}
