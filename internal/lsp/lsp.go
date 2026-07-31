// Package lsp provides LSP-backed code-intelligence tools. The daemon does not
// run a parallel live-LSP stack: it prefers editor-fresh data, querying the
// session's bound editor for open buffers. The optional workspace index for
// closed files is not part of milestone 0, so a headless session (no bound
// editor) returns editor_unavailable rather than spinning up LSP clients.
package lsp

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/approvals"
	"github.com/dusto/tend/internal/session"
	"github.com/dusto/tend/internal/worktree"
)

// Method names.
const (
	MethodDiagnostics = "lsp.diagnostics"
	MethodSymbols     = "lsp.symbols"
	MethodDefinition  = "lsp.definition"
	MethodReferences  = "lsp.references"
	MethodHover       = "lsp.hover"
	MethodCodeActions = "lsp.code_actions"
)

// Errors returned by the LSP service.
var (
	// ErrNoSession reports that no session has the given id.
	ErrNoSession = errors.New("lsp: unknown session")
	// ErrAccessDenied reports that a gated outside-worktree read was refused —
	// the user denied the filesystem_access approval, or the resolved target
	// changed between approval and read (a symlink repoint).
	ErrAccessDenied = errors.New("lsp: filesystem access denied")
)

// approver gates an outside-worktree read on a session. *approvals.Gate
// satisfies it.
type approver interface {
	Request(ctx context.Context, sess *session.Session, kind string, detail json.RawMessage) (approvals.Outcome, error)
}

// Options configures a Service.
type Options struct {
	// ExtraReadableRoots are directories outside the worktree that reads resolve
	// as in-scope WITHOUT prompting (e.g. the module cache). Conservative by
	// default (empty). Shared with the file service's setting.
	ExtraReadableRoots []string
	// PromptCapable reports whether any connected client can answer an approval.
	// An outside read with no prompt-capable client hard-denies instead of
	// blocking forever on an unanswerable prompt. Nil means "assume prompting is
	// available" (the daemon always wires it; nil is for tests).
	PromptCapable func() bool
}

// editorClient is the slice of editor.Service the LSP tools drive: resolving
// the current buffer and querying editor-fresh diagnostics. It is an interface
// so the tools can be tested without a live editor connection.
type editorClient interface {
	CurrentBuffer(ctx context.Context, sessionID api.SessionID) (api.EditorCurrentBufferResult, error)
	Diagnostics(ctx context.Context, sessionID api.SessionID, p api.EditorDiagnosticsParams) (api.EditorDiagnosticsResult, error)
	Symbols(ctx context.Context, sessionID api.SessionID, p api.EditorSymbolsParams) (api.EditorSymbolsResult, error)
	Definition(ctx context.Context, sessionID api.SessionID, p api.EditorDefinitionParams) (api.EditorDefinitionResult, error)
	References(ctx context.Context, sessionID api.SessionID, p api.EditorReferencesParams) (api.EditorReferencesResult, error)
	Hover(ctx context.Context, sessionID api.SessionID, p api.EditorHoverParams) (api.EditorHoverResult, error)
	CodeActions(ctx context.Context, sessionID api.SessionID, p api.EditorCodeActionsParams) (api.EditorCodeActionsResult, error)
}

// Service implements the LSP tools over the editor reverse-RPC. It is safe for
// concurrent use (it holds no mutable state of its own).
type Service struct {
	sessions      *session.Registry
	editors       editorClient
	approver      approver
	extraRoots    []string
	promptCapable func() bool
}

// NewService returns a Service routing through the editor reverse-RPC, with the
// session registry for worktree-boundary checks. gate may be nil, in which case
// an outside-worktree read stays hard-denied (it cannot be gated).
func NewService(sessions *session.Registry, editors editorClient, gate approver, opts Options) *Service {
	return &Service{
		sessions:      sessions,
		editors:       editors,
		approver:      gate,
		extraRoots:    opts.ExtraReadableRoots,
		promptCapable: opts.PromptCapable,
	}
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
	uri, skip, err := s.resolveTarget(ctx, sess, p.URI, MethodDiagnostics)
	if err != nil {
		return api.LSPDiagnosticsResult{}, err
	}
	if skip {
		return api.LSPDiagnosticsResult{Diagnostics: []api.Diagnostic{}}, nil
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

// Symbols returns editor-fresh document symbols for a file (whole-file outline).
// Empty uri resolves the current buffer; boundary rules match Diagnostics.
func (s *Service) Symbols(ctx context.Context, p api.LSPSymbolsParams) (api.LSPSymbolsResult, error) {
	sess, ok := s.sessions.Get(p.SessionID)
	if !ok {
		return api.LSPSymbolsResult{}, ErrNoSession
	}
	uri, skip, err := s.resolveTarget(ctx, sess, p.URI, MethodSymbols)
	if err != nil {
		return api.LSPSymbolsResult{}, err
	}
	if skip {
		return api.LSPSymbolsResult{Symbols: []api.DocumentSymbol{}}, nil
	}
	res, err := s.editors.Symbols(ctx, p.SessionID, api.EditorSymbolsParams{URI: uri})
	if err != nil {
		return api.LSPSymbolsResult{}, err
	}
	if res.Symbols == nil {
		res.Symbols = []api.DocumentSymbol{}
	}
	return api.LSPSymbolsResult(res), nil
}

// Definition returns editor-fresh definition location(s) of the symbol at a
// position. Result locations may point outside the worktree (a dependency or the
// standard library); only the input file is worktree-bounded.
func (s *Service) Definition(ctx context.Context, p api.LSPDefinitionParams) (api.LSPDefinitionResult, error) {
	sess, ok := s.sessions.Get(p.SessionID)
	if !ok {
		return api.LSPDefinitionResult{}, ErrNoSession
	}
	uri, skip, err := s.resolveTarget(ctx, sess, p.URI, MethodDefinition)
	if err != nil {
		return api.LSPDefinitionResult{}, err
	}
	if skip {
		return api.LSPDefinitionResult{Locations: []api.Location{}}, nil
	}
	res, err := s.editors.Definition(ctx, p.SessionID, api.EditorDefinitionParams{URI: uri, Position: p.Position})
	if err != nil {
		return api.LSPDefinitionResult{}, err
	}
	return api.LSPDefinitionResult{URI: res.URI, Open: res.Open, Locations: nonNilLocations(res.Locations)}, nil
}

// References returns editor-fresh reference locations of the symbol at a
// position. include_declaration is forwarded to the editor.
func (s *Service) References(ctx context.Context, p api.LSPReferencesParams) (api.LSPReferencesResult, error) {
	sess, ok := s.sessions.Get(p.SessionID)
	if !ok {
		return api.LSPReferencesResult{}, ErrNoSession
	}
	uri, skip, err := s.resolveTarget(ctx, sess, p.URI, MethodReferences)
	if err != nil {
		return api.LSPReferencesResult{}, err
	}
	if skip {
		return api.LSPReferencesResult{Locations: []api.Location{}}, nil
	}
	res, err := s.editors.References(ctx, p.SessionID, api.EditorReferencesParams{
		URI: uri, Position: p.Position, IncludeDeclaration: p.IncludeDeclaration,
	})
	if err != nil {
		return api.LSPReferencesResult{}, err
	}
	return api.LSPReferencesResult{URI: res.URI, Open: res.Open, Locations: nonNilLocations(res.Locations)}, nil
}

// Hover returns editor-fresh hover info for the symbol at a position.
func (s *Service) Hover(ctx context.Context, p api.LSPHoverParams) (api.LSPHoverResult, error) {
	sess, ok := s.sessions.Get(p.SessionID)
	if !ok {
		return api.LSPHoverResult{}, ErrNoSession
	}
	uri, skip, err := s.resolveTarget(ctx, sess, p.URI, MethodHover)
	if err != nil {
		return api.LSPHoverResult{}, err
	}
	if skip {
		return api.LSPHoverResult{}, nil
	}
	res, err := s.editors.Hover(ctx, p.SessionID, api.EditorHoverParams{URI: uri, Position: p.Position})
	if err != nil {
		return api.LSPHoverResult{}, err
	}
	return api.LSPHoverResult(res), nil
}

// CodeActions lists the editor-fresh code actions available for a range. It is
// list-only: each edit-carrying action includes change-set-ready edits, but
// nothing is applied here — the caller submits a chosen action's Changes to
// file.apply_change_set (which gates and reviews it as a file edit, and
// re-enforces the worktree boundary on every target). The input file is
// worktree-bounded exactly as the other LSP tools.
func (s *Service) CodeActions(ctx context.Context, p api.LSPCodeActionsParams) (api.LSPCodeActionsResult, error) {
	sess, ok := s.sessions.Get(p.SessionID)
	if !ok {
		return api.LSPCodeActionsResult{}, ErrNoSession
	}
	uri, skip, err := s.resolveTarget(ctx, sess, p.URI, MethodCodeActions)
	if err != nil {
		return api.LSPCodeActionsResult{}, err
	}
	if skip {
		return api.LSPCodeActionsResult{Actions: []api.CodeAction{}}, nil
	}
	res, err := s.editors.CodeActions(ctx, p.SessionID, api.EditorCodeActionsParams{URI: uri, Range: p.Range, Only: p.Only})
	if err != nil {
		return api.LSPCodeActionsResult{}, err
	}
	if res.Actions == nil {
		res.Actions = []api.CodeAction{}
	}
	return api.LSPCodeActionsResult(res), nil
}

// resolveTarget resolves the file a query targets and enforces the worktree
// boundary, exactly as lsp.diagnostics does. It returns the uri to query;
// skip=true means there is nothing in scope (an empty or out-of-worktree current
// buffer) and the caller should return an empty result without querying the
// editor. An explicitly named out-of-worktree uri is an error (a refused
// boundary crossing), so an agent for one repo cannot navigate another's files.
func (s *Service) resolveTarget(ctx context.Context, sess *session.Session, uri, tool string) (resolved string, skip bool, err error) {
	if uri == "" {
		cur, err := s.editors.CurrentBuffer(ctx, sess.ID)
		if err != nil {
			return "", false, err
		}
		if cur.URI == "" || !worktree.Contains(cur.URI, sess.WorktreeRoot) {
			return "", true, nil
		}
		return cur.URI, false, nil
	}
	if err := s.authorizeRead(ctx, sess, uri, tool); err != nil {
		return "", false, err
	}
	return uri, false, nil
}

// authorizeRead enforces the worktree read boundary as a consent boundary: an
// in-worktree (or extra-readable-root) uri is allowed as before; an
// outside-worktree uri raises a filesystem_access approval (diagnostics mode)
// and blocks like any gated action. On approval it re-resolves and requires the
// SAME target (a symlink repointed between prompt and read is refused — TOCTOU);
// on denial it returns ErrAccessDenied. With no approver wired, or no
// prompt-capable client to answer (headless/CLI-only), the outside path stays
// hard-denied rather than blocking on an unanswerable prompt. All LSP navigation
// reads route through here, so they gate uniformly; tool names the calling
// method so the approval reflects the concrete operation.
func (s *Service) authorizeRead(ctx context.Context, sess *session.Session, uri, tool string) error {
	resolved, inside, err := worktree.ClassifyPath(uri, sess.WorktreeRoot, s.extraRoots...)
	if err != nil {
		return err
	}
	if inside {
		return nil
	}
	if s.approver == nil || (s.promptCapable != nil && !s.promptCapable()) {
		return worktree.ErrOutsideWorkspace
	}
	detail, _ := json.Marshal(api.ApprovalDetail{
		Kind: api.ApprovalFilesystemAccess,
		FilesystemAccess: &api.FilesystemAccessApproval{
			RequestedURI: uri,
			ResolvedPath: resolved,
			Mode:         api.FilesystemModeDiagnostics,
			Tool:         tool,
		},
	})
	outcome, err := s.approver.Request(ctx, sess, api.ApprovalFilesystemAccess, detail)
	if err != nil {
		return err
	}
	if !outcome.Approved {
		return ErrAccessDenied
	}
	reResolved, _, err := worktree.ClassifyPath(uri, sess.WorktreeRoot, s.extraRoots...)
	if err != nil {
		return err
	}
	if reResolved != resolved {
		return ErrAccessDenied
	}
	return nil
}

// nonNilLocations returns locs, or an empty non-nil slice, so a result marshals
// as [] rather than null.
func nonNilLocations(locs []api.Location) []api.Location {
	if locs == nil {
		return []api.Location{}
	}
	return locs
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
