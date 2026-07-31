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
	if err := dispatch.Handle(m, MethodDiagnostics, func(ctx context.Context, p api.LSPDiagnosticsParams) (api.LSPDiagnosticsResult, error) {
		res, err := s.Diagnostics(ctx, p)
		if err != nil {
			return api.LSPDiagnosticsResult{}, toRPCError(err)
		}
		return res, nil
	}); err != nil {
		return err
	}
	if err := dispatch.Handle(m, MethodSymbols, func(ctx context.Context, p api.LSPSymbolsParams) (api.LSPSymbolsResult, error) {
		res, err := s.Symbols(ctx, p)
		if err != nil {
			return api.LSPSymbolsResult{}, toRPCError(err)
		}
		return res, nil
	}); err != nil {
		return err
	}
	if err := dispatch.Handle(m, MethodDefinition, func(ctx context.Context, p api.LSPDefinitionParams) (api.LSPDefinitionResult, error) {
		res, err := s.Definition(ctx, p)
		if err != nil {
			return api.LSPDefinitionResult{}, toRPCError(err)
		}
		return res, nil
	}); err != nil {
		return err
	}
	if err := dispatch.Handle(m, MethodReferences, func(ctx context.Context, p api.LSPReferencesParams) (api.LSPReferencesResult, error) {
		res, err := s.References(ctx, p)
		if err != nil {
			return api.LSPReferencesResult{}, toRPCError(err)
		}
		return res, nil
	}); err != nil {
		return err
	}
	if err := dispatch.Handle(m, MethodHover, func(ctx context.Context, p api.LSPHoverParams) (api.LSPHoverResult, error) {
		res, err := s.Hover(ctx, p)
		if err != nil {
			return api.LSPHoverResult{}, toRPCError(err)
		}
		return res, nil
	}); err != nil {
		return err
	}
	return dispatch.Handle(m, MethodCodeActions, func(ctx context.Context, p api.LSPCodeActionsParams) (api.LSPCodeActionsResult, error) {
		res, err := s.CodeActions(ctx, p)
		if err != nil {
			return api.LSPCodeActionsResult{}, toRPCError(err)
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
		errors.Is(err, worktree.ErrBadURI), errors.Is(err, worktree.ErrOutsideWorkspace),
		errors.Is(err, ErrAccessDenied):
		return &rpc.Error{Code: rpc.CodeInvalidParams, Message: err.Error()}
	default:
		return &rpc.Error{Code: rpc.CodeInternalError, Message: err.Error()}
	}
}
