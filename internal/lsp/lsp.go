// Package lsp provides LSP-backed code-intelligence tools. The daemon does not
// run a parallel live-LSP stack: it prefers editor-fresh data, querying the
// session's bound editor for open buffers. The optional workspace index for
// closed files is not part of milestone 0, so a headless session (no bound
// editor) returns editor_unavailable rather than spinning up LSP clients.
package lsp

import (
	"context"
	"errors"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/session"
	"github.com/dusto/tend/internal/worktree"
)

// Method names.
const (
	MethodDiagnostics = "lsp.diagnostics"
)

// ErrNoSession reports that no session has the given id.
var ErrNoSession = errors.New("lsp: unknown session")

// editorClient is the slice of editor.Service the LSP tools drive: resolving
// the current buffer and querying editor-fresh diagnostics. It is an interface
// so the tools can be tested without a live editor connection.
type editorClient interface {
	CurrentBuffer(ctx context.Context, sessionID api.SessionID) (api.EditorCurrentBufferResult, error)
	Diagnostics(ctx context.Context, sessionID api.SessionID, p api.EditorDiagnosticsParams) (api.EditorDiagnosticsResult, error)
}

// Service implements the LSP tools over the editor reverse-RPC. It is safe for
// concurrent use (it holds no mutable state of its own).
type Service struct {
	sessions *session.Registry
	editors  editorClient
}

// NewService returns a Service routing through the editor reverse-RPC, with the
// session registry for worktree-boundary checks.
func NewService(sessions *session.Registry, editors editorClient) *Service {
	return &Service{sessions: sessions, editors: editors}
}

// Diagnostics returns editor-fresh diagnostics for a file. With an empty URI it
// resolves the editor's current buffer first; a buffer with no file-backed URI
// yields an empty result. Severity, when set, filters to that level or more
// severe. A headless session surfaces as editor_unavailable from the editor
// service.
func (s *Service) Diagnostics(ctx context.Context, p api.LSPDiagnosticsParams) (api.LSPDiagnosticsResult, error) {
	sess, ok := s.sessions.Get(p.SessionID)
	if !ok {
		return api.LSPDiagnosticsResult{}, ErrNoSession
	}
	empty := api.LSPDiagnosticsResult{Diagnostics: []api.Diagnostic{}}

	uri := p.URI
	if uri == "" {
		cur, err := s.editors.CurrentBuffer(ctx, p.SessionID)
		if err != nil {
			return api.LSPDiagnosticsResult{}, err
		}
		// The current buffer may be any file the user has focused. Only diagnose
		// it when it is inside this session's worktree; otherwise there is simply
		// nothing in scope to report (an out-of-worktree buffer is not the agent's
		// to read, and naming nothing must not leak where the user is).
		if cur.URI == "" || !worktree.Contains(cur.URI, sess.WorktreeRoot) {
			return empty, nil
		}
		uri = cur.URI
	} else if _, err := worktree.ResolvePath(uri, sess.WorktreeRoot); err != nil {
		// An explicitly named path outside the worktree is a boundary violation,
		// reported like the file tools rather than silently emptied.
		return api.LSPDiagnosticsResult{}, err
	}

	res, err := s.editors.Diagnostics(ctx, p.SessionID, api.EditorDiagnosticsParams{URI: uri})
	if err != nil {
		return api.LSPDiagnosticsResult{}, err
	}
	return api.LSPDiagnosticsResult{
		URI:         res.URI,
		Open:        res.Open,
		Diagnostics: filterSeverity(res.Diagnostics, p.Severity),
	}, nil
}

// severityRank orders severities most-severe-first so a minimum-severity filter
// is a simple rank comparison. An unknown value sorts last (least severe).
func severityRank(s api.DiagnosticSeverity) int {
	switch s {
	case api.SeverityError:
		return 0
	case api.SeverityWarning:
		return 1
	case api.SeverityInfo:
		return 2
	case api.SeverityHint:
		return 3
	default:
		return 4
	}
}

// filterSeverity keeps diagnostics at least as severe as min. An empty min
// keeps everything. The result is always non-nil so it marshals as [] not null.
func filterSeverity(diags []api.Diagnostic, min api.DiagnosticSeverity) []api.Diagnostic {
	out := make([]api.Diagnostic, 0, len(diags))
	if min == "" {
		return append(out, diags...)
	}
	limit := severityRank(min)
	for _, d := range diags {
		if severityRank(d.Severity) <= limit {
			out = append(out, d)
		}
	}
	return out
}
