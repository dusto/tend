package lsp

import (
	"context"
	"errors"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/dispatch"
	"github.com/dusto/tend/internal/editor"
	"github.com/dusto/tend/internal/rpc"
	"github.com/dusto/tend/internal/worktree"
)

// Register installs the LSP methods on m, backed by s.
func Register(m *dispatch.Mux, s *Service) error {
	return dispatch.Handle(m, MethodDiagnostics, func(ctx context.Context, p api.LSPDiagnosticsParams) (api.LSPDiagnosticsResult, error) {
		res, err := s.Diagnostics(ctx, p)
		if err != nil {
			return api.LSPDiagnosticsResult{}, toRPCError(err)
		}
		return res, nil
	})
}

// toRPCError maps an LSP-service error to the JSON-RPC error sent to the
// client. A headless session becomes editor_unavailable; an unknown session or
// an out-of-worktree uri is invalid params (the latter a refused boundary
// crossing, mapped like the file tools).
func toRPCError(err error) error {
	switch {
	case errors.Is(err, editor.ErrEditorUnavailable):
		return &rpc.Error{Code: api.ErrEditorUnavailable, Message: err.Error()}
	case errors.Is(err, ErrNoSession), errors.Is(err, editor.ErrNoSession),
		errors.Is(err, worktree.ErrBadURI), errors.Is(err, worktree.ErrOutsideWorkspace):
		return &rpc.Error{Code: rpc.CodeInvalidParams, Message: err.Error()}
	default:
		return &rpc.Error{Code: rpc.CodeInternalError, Message: err.Error()}
	}
}
