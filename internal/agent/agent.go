// Package agent implements the editor-facing agent lifecycle methods
// (agent.start/prompt/cancel/stop). It is the daemon-wide convergence point: it
// drives the ACP session manager to open and prompt task-scoped sessions, tracks
// each session's authoritative state in the session registry, and lets the
// installed normalizer stream a turn's output as events on the session's stream.
// Sessions are workspace-bound and outlive any single client connection, so one
// Service is shared across connections.
package agent

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/acp"
	"github.com/dusto/tend/internal/dispatch"
	"github.com/dusto/tend/internal/rpc"
	"github.com/dusto/tend/internal/session"
)

// Method names for the agent lifecycle, matching the api contract.
const (
	MethodStart  = "agent.start"
	MethodPrompt = "agent.prompt"
	MethodCancel = "agent.cancel"
	MethodStop   = "agent.stop"
)

// Manager is the slice of the ACP session manager the service drives. *acp.Manager
// satisfies it; it is an interface so the lifecycle orchestration can be tested
// without spawning provider processes.
type Manager interface {
	Open(ctx context.Context, key acp.Key, params acp.NewSessionParams) (*acp.Session, error)
	Prompt(ctx context.Context, id api.SessionID, params acp.PromptParams) (acp.PromptResult, error)
	Cancel(ctx context.Context, id api.SessionID) error
	Close(id api.SessionID)
}

// Service backs the agent.* methods. It is safe for concurrent use.
type Service struct {
	sessions *session.Registry
	manager  Manager
	norm     *acp.Normalizer

	mu       sync.Mutex
	inflight map[api.SessionID]context.CancelFunc
}

// NewService returns a Service that opens sessions through manager, records their
// state in sessions, and ends turns through norm (which publishes turn_end).
func NewService(sessions *session.Registry, manager Manager, norm *acp.Normalizer) *Service {
	return &Service{
		sessions: sessions,
		manager:  manager,
		norm:     norm,
		inflight: make(map[api.SessionID]context.CancelFunc),
	}
}

// Register installs the agent lifecycle methods on m, backed by s.
// Register installs the agent methods on m. onStarted, when non-nil, is called
// with a new session's id after agent.start succeeds — the per-connection hook
// the daemon uses to bind the session to its creating editor client. It runs
// only on success and does not affect the start result.
func Register(m *dispatch.Mux, s *Service, onStarted func(api.SessionID)) error {
	start := s.Start
	if onStarted != nil {
		start = func(ctx context.Context, p api.AgentStartParams) (api.AgentStartResult, error) {
			res, err := s.Start(ctx, p)
			if err == nil {
				onStarted(res.SessionID)
			}
			return res, err
		}
	}
	if err := dispatch.Handle(m, MethodStart, start); err != nil {
		return err
	}
	if err := dispatch.Handle(m, MethodPrompt, s.Prompt); err != nil {
		return err
	}
	if err := dispatch.Handle(m, MethodCancel, s.Cancel); err != nil {
		return err
	}
	return dispatch.Handle(m, MethodStop, s.Stop)
}

// Start opens a task-scoped session on a provider process for the task's
// workspace, registers its authoritative state, and returns the session id and
// the stream its events arrive on.
func (s *Service) Start(ctx context.Context, p api.AgentStartParams) (api.AgentStartResult, error) {
	if p.ProviderID == "" {
		return api.AgentStartResult{}, invalidParams("provider_id is required")
	}
	if p.WorktreeRoot == "" {
		return api.AgentStartResult{}, invalidParams("worktree_root is required")
	}
	// Task is optional. When present (any field set) it must be complete, since a
	// partial task ref is meaningless. When absent the session is task-less.
	hasTask := p.Task.ID != "" || p.Task.Provider != "" || p.Task.WorkspaceID != ""
	if hasTask {
		switch {
		case p.Task.Provider == "":
			return api.AgentStartResult{}, invalidParams("task.provider is required")
		case p.Task.WorkspaceID == "":
			return api.AgentStartResult{}, invalidParams("task.workspace_id is required")
		case p.Task.ID == "":
			return api.AgentStartResult{}, invalidParams("task.id is required")
		}
	}
	// The workspace comes from the explicit field, falling back to the task's
	// for a task-bound start; one of them must supply it. When both are set they
	// must agree — a session bound to one workspace cannot authorize work in
	// another, so a mismatch is a caller error rather than a silent precedence.
	workspace := p.WorkspaceID
	switch {
	case workspace == "":
		workspace = p.Task.WorkspaceID
	case hasTask && p.Task.WorkspaceID != workspace:
		return api.AgentStartResult{}, invalidParams("workspace_id and task.workspace_id must match")
	}
	if workspace == "" {
		return api.AgentStartResult{}, invalidParams("workspace_id is required (set workspace_id or task)")
	}

	key := acp.Key{Workspace: workspace, Provider: p.ProviderID}
	// Carry the worktree root so a provider process spawned for this session
	// starts in it; the session's own cwd is set on session/new below.
	ctx = acp.WithWorktreeRoot(ctx, p.WorktreeRoot)
	as, err := s.manager.Open(ctx, key, acp.NewSessionParams{Cwd: p.WorktreeRoot})
	if err != nil {
		return api.AgentStartResult{}, internalErr(err)
	}

	// The provider assigns the session id; reusing one we already track would
	// collide in the registry, so refuse it rather than panic.
	if _, dup := s.sessions.Get(as.ID); dup {
		s.manager.Close(as.ID)
		return api.AgentStartResult{}, &rpc.Error{Code: rpc.CodeInternalError, Message: "agent: duplicate session id " + string(as.ID)}
	}
	sess := s.sessions.Create(as.ID, p.ProviderID, workspace, p.Task, p.WorktreeRoot)
	return api.AgentStartResult{SessionID: sess.ID, StreamID: sess.Stream, Status: sess.Status()}, nil
}

// Prompt runs one prompt turn on a session. It blocks until the turn ends; the
// turn's output streams as events on the session's stream via the normalizer
// installed on the process. On success it publishes turn_end and returns the
// session to idle; a cancelled turn also returns to idle; any other failure marks
// the session errored. A prompt on an errored session first recovers it to idle,
// so a failed turn can be retried.
func (s *Service) Prompt(ctx context.Context, p api.AgentPromptParams) (api.AgentPromptResult, error) {
	sess, ok := s.sessions.Get(p.SessionID)
	if !ok {
		return api.AgentPromptResult{}, unknownSession(p.SessionID)
	}
	// Recover an errored session: the state model requires error -> idle before a
	// new turn can start (idle -> running), so a retry passes through idle.
	if sess.Status() == api.StatusError {
		if err := sess.SetStatus(api.StatusIdle, nil); err != nil {
			return api.AgentPromptResult{}, invalidParams(err.Error())
		}
	}
	if err := sess.SetStatus(api.StatusRunning, nil); err != nil {
		return api.AgentPromptResult{}, invalidParams(err.Error())
	}

	turnCtx, cancel := context.WithCancel(ctx)
	s.setInflight(p.SessionID, cancel)
	defer s.clearInflight(p.SessionID)
	defer cancel()

	params := acp.PromptParams{Prompt: []json.RawMessage{textBlock(p.Text)}}
	res, err := s.manager.Prompt(turnCtx, p.SessionID, params)
	if err != nil {
		// A turn cancelled through agent.cancel (turnCtx cancelled while the
		// caller's ctx is still live) returns the session to idle, not error.
		if turnCtx.Err() != nil && ctx.Err() == nil {
			_ = sess.SetStatus(api.StatusIdle, nil)
			return api.AgentPromptResult{StopReason: "cancelled", Status: sess.Status()}, nil
		}
		_ = sess.SetStatus(api.StatusError, nil)
		return api.AgentPromptResult{}, internalErr(err)
	}

	s.norm.PublishTurnEnd(string(p.SessionID))
	_ = sess.SetStatus(api.StatusIdle, nil)
	return api.AgentPromptResult{StopReason: res.StopReason, Status: api.StatusIdle}, nil
}

// Cancel aborts the in-flight turn on a session: it signals the agent through ACP
// and cancels the local turn so Prompt returns. The Prompt call returns the
// session to idle. Cancelling a session with no in-flight turn is a no-op.
func (s *Service) Cancel(ctx context.Context, p api.AgentCancelParams) (struct{}, error) {
	if _, ok := s.sessions.Get(p.SessionID); !ok {
		return struct{}{}, unknownSession(p.SessionID)
	}
	_ = s.manager.Cancel(ctx, p.SessionID) // best-effort signal to the agent
	s.cancelInflight(p.SessionID)
	return struct{}{}, nil
}

// Stop ends a session: it cancels any in-flight turn, marks the session ended,
// releases its hold on the provider process, and drops it from the registry.
func (s *Service) Stop(ctx context.Context, p api.AgentStopParams) (struct{}, error) {
	sess, ok := s.sessions.Get(p.SessionID)
	if !ok {
		return struct{}{}, unknownSession(p.SessionID)
	}
	_ = s.manager.Cancel(ctx, p.SessionID) // best-effort signal to the agent
	s.cancelInflight(p.SessionID)
	_ = sess.SetStatus(api.StatusEnded, nil)
	s.manager.Close(p.SessionID)
	s.sessions.Remove(p.SessionID)
	return struct{}{}, nil
}

func (s *Service) setInflight(id api.SessionID, cancel context.CancelFunc) {
	s.mu.Lock()
	s.inflight[id] = cancel
	s.mu.Unlock()
}

func (s *Service) clearInflight(id api.SessionID) {
	s.mu.Lock()
	delete(s.inflight, id)
	s.mu.Unlock()
}

func (s *Service) cancelInflight(id api.SessionID) {
	s.mu.Lock()
	cancel := s.inflight[id]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// textBlock builds an ACP text content block for a prompt.
func textBlock(text string) json.RawMessage {
	b, _ := json.Marshal(struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{Type: "text", Text: text})
	return b
}

func invalidParams(msg string) *rpc.Error {
	return &rpc.Error{Code: rpc.CodeInvalidParams, Message: "agent: " + msg}
}

func internalErr(err error) *rpc.Error {
	return &rpc.Error{Code: rpc.CodeInternalError, Message: err.Error()}
}

func unknownSession(id api.SessionID) *rpc.Error {
	return &rpc.Error{Code: rpc.CodeInvalidParams, Message: "agent: unknown session " + string(id)}
}
