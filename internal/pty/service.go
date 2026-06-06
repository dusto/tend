package pty

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/approvals"
	"github.com/dusto/tend/internal/dispatch"
	"github.com/dusto/tend/internal/rpc"
	"github.com/dusto/tend/internal/session"
)

// Pane method names.
const (
	MethodOpen  = "pane.open"
	MethodList  = "pane.list"
	MethodRead  = "pane.read"
	MethodClose = "pane.close"
)

// ErrNoSession reports that an agent-initiated open named an unknown session.
var ErrNoSession = errors.New("pty: unknown session")

// ErrNoPane reports that no pane has the given id.
var ErrNoPane = errors.New("pty: unknown pane")

// ErrDenied reports that an agent-initiated open was denied at the approval gate.
var ErrDenied = errors.New("pty: pane open denied")

// approver gates an agent-initiated open. *approvals.Gate satisfies it.
type approver interface {
	Request(ctx context.Context, sess *session.Session, kind string, detail json.RawMessage) (approvals.Outcome, error)
}

// Service backs the pane.* methods over the PTY Manager. A pane.open carrying a
// session is agent-initiated and routed through the approval gate; opens, lists,
// reads, and closes otherwise run directly. It is safe for concurrent use.
type Service struct {
	mgr      *Manager
	sessions *session.Registry
	approver approver
	shell    string
}

// NewService returns a Service. shell is the program idle panes run; empty uses
// $SHELL then /bin/sh.
func NewService(mgr *Manager, sessions *session.Registry, gate approver, shell string) *Service {
	if shell == "" {
		shell = os.Getenv("SHELL")
	}
	if shell == "" {
		shell = "/bin/sh"
	}
	return &Service{mgr: mgr, sessions: sessions, approver: gate, shell: shell}
}

// Register installs the pane methods on m, backed by s.
func Register(m *dispatch.Mux, s *Service) error {
	for _, reg := range []func() error{
		func() error { return dispatch.Handle(m, MethodOpen, s.open) },
		func() error { return dispatch.Handle(m, MethodList, s.list) },
		func() error { return dispatch.Handle(m, MethodRead, s.read) },
		func() error { return dispatch.Handle(m, MethodClose, s.close) },
	} {
		if err := reg(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) open(ctx context.Context, p api.PaneOpenParams) (api.PaneInfo, error) {
	// A session in context makes this agent-initiated: the pane is bound to the
	// session's authoritative workspace and worktree (not caller-supplied values),
	// and the open is approval-gated, since it allocates a shell and changes
	// terminal UI even with no command run. A user-initiated open (no session) is
	// a deliberate user action — opening a shell — so it honors the caller's
	// workspace/worktree/cwd as given.
	ws, worktree, cwd := p.WorkspaceID, p.WorktreeRoot, p.Cwd

	if p.SessionID != "" {
		sess, ok := s.sessions.Get(p.SessionID)
		if !ok {
			return api.PaneInfo{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: ErrNoSession.Error()}
		}
		ws, worktree = sess.Task.WorkspaceID, sess.WorktreeRoot
		if cwd == "" {
			cwd = worktree
		}
		// The cwd (default worktree root, or an explicit override that may be
		// outside the repo) travels in the approval detail, so it is part of the
		// user's decision rather than silently trusted.
		detail, _ := json.Marshal(api.ApprovalDetail{
			Kind:     api.ApprovalPaneOpen,
			PaneOpen: &api.PaneOpenApproval{WorkspaceID: ws, Cwd: cwd},
		})
		outcome, err := s.approver.Request(ctx, sess, api.ApprovalPaneOpen, detail)
		if err != nil {
			return api.PaneInfo{}, err
		}
		if !outcome.Approved {
			return api.PaneInfo{}, &rpc.Error{Code: rpc.CodeInvalidRequest, Message: ErrDenied.Error()}
		}
	} else if cwd == "" {
		cwd = worktree
	}

	pane, err := s.mgr.Spawn(SpawnConfig{
		Command:      s.shell,
		Dir:          cwd,
		Workspace:    ws,
		WorktreeRoot: worktree,
	})
	if err != nil {
		return api.PaneInfo{}, &rpc.Error{Code: rpc.CodeInternalError, Message: err.Error()}
	}
	return paneInfo(pane), nil
}

func (s *Service) list(_ context.Context, p api.PaneListParams) (api.PaneListResult, error) {
	out := make([]api.PaneInfo, 0)
	for _, pane := range s.mgr.List() {
		if p.WorkspaceID != "" && pane.Workspace != p.WorkspaceID {
			continue
		}
		out = append(out, paneInfo(pane))
	}
	return api.PaneListResult{Panes: out}, nil
}

func (s *Service) read(_ context.Context, p api.PaneReadParams) (api.PaneReadResult, error) {
	pane, ok := s.mgr.Get(p.PaneID)
	if !ok {
		return api.PaneReadResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: ErrNoPane.Error()}
	}
	data := pane.Scrollback()
	if p.Tail > 0 && len(data) > p.Tail {
		data = data[len(data)-p.Tail:]
	}
	return api.PaneReadResult{PaneID: p.PaneID, Data: data, Running: pane.Running()}, nil
}

func (s *Service) close(_ context.Context, p api.PaneCloseParams) (api.PaneCloseResult, error) {
	if !s.mgr.Close(p.PaneID) {
		return api.PaneCloseResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: ErrNoPane.Error()}
	}
	return api.PaneCloseResult{}, nil
}

// paneInfo converts a Pane to its wire view. Until the terminal bridge lands, a
// pane has no visible view, so it is always detached.
func paneInfo(p *Pane) api.PaneInfo {
	code, exited := p.ExitCode()
	return api.PaneInfo{
		PaneID:       p.ID,
		WorkspaceID:  p.Workspace,
		WorktreeRoot: p.WorktreeRoot,
		Cwd:          p.Cwd,
		Running:      !exited,
		ExitCode:     code,
		ViewState:    api.PaneDetached,
	}
}
