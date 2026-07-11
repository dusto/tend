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
	"fmt"
	"sync"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/acp"
	"github.com/dusto/tend/internal/dispatch"
	"github.com/dusto/tend/internal/rpc"
	"github.com/dusto/tend/internal/session"
)

// Method names for the agent lifecycle, matching the api contract. set_mode and
// set_model are named session.* but live here because they drive the ACP manager
// and publish session events, which the session-query service does not own.
const (
	MethodStart           = "agent.start"
	MethodPrompt          = "agent.prompt"
	MethodCancel          = "agent.cancel"
	MethodStop            = "agent.stop"
	MethodSetMode         = "session.set_mode"
	MethodSetModel        = "session.set_model"
	MethodSetThoughtLevel = "session.set_thought_level"
)

// Manager is the slice of the ACP session manager the service drives. *acp.Manager
// satisfies it; it is an interface so the lifecycle orchestration can be tested
// without spawning provider processes.
type Manager interface {
	Open(ctx context.Context, key acp.Key, params acp.NewSessionParams) (*acp.Session, error)
	Prompt(ctx context.Context, id api.SessionID, params acp.PromptParams) (acp.PromptResult, error)
	Cancel(ctx context.Context, id api.SessionID) error
	Close(id api.SessionID)
	SetMode(ctx context.Context, id api.SessionID, modeID string) error
	SetModel(ctx context.Context, id api.SessionID, modelID string) error
	SetThoughtLevel(ctx context.Context, id api.SessionID, thoughtLevelID string) error
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
	if err := dispatch.Handle(m, MethodStop, s.Stop); err != nil {
		return err
	}
	if err := dispatch.Handle(m, MethodSetMode, s.SetMode); err != nil {
		return err
	}
	if err := dispatch.Handle(m, MethodSetModel, s.SetModel); err != nil {
		return err
	}
	return dispatch.Handle(m, MethodSetThoughtLevel, s.SetThoughtLevel)
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
	// Record the provider's advertised mode/model/thought-level choices (empty
	// when it offers none) so session.list reports them and the set_* methods can
	// validate.
	sess.SetModes(as.CurrentModeID, as.AvailableModes)
	sess.SetModels(as.CurrentModelID, as.AvailableModels)
	sess.SetThoughtLevels(as.CurrentThoughtLevelID, as.AvailableThoughtLevels)
	return api.AgentStartResult{SessionID: sess.ID, StreamID: sess.Stream, Status: sess.Status()}, nil
}

// SetMode sets a session's active mode (behavior/permission mode) on its
// provider and records it, emitting agent_mode_updated. It rejects an unknown
// session, an empty mode id, and a mode the provider did not advertise — so a
// provider that offers no modes degrades to a clear invalid-params error rather
// than a silent no-op or a bad ACP call.
func (s *Service) SetMode(ctx context.Context, p api.SessionSetModeParams) (api.SessionSetModeResult, error) {
	sess, ok := s.sessions.Get(p.SessionID)
	if !ok {
		return api.SessionSetModeResult{}, unknownSession(p.SessionID)
	}
	if p.ModeID == "" {
		return api.SessionSetModeResult{}, invalidParams("mode_id is required")
	}
	_, available := sess.Modes()
	if !hasID(available, p.ModeID, func(m api.SessionMode) string { return m.ID }) {
		return api.SessionSetModeResult{}, invalidParams("session " + string(p.SessionID) + " has no mode " + p.ModeID)
	}
	if err := s.manager.SetMode(ctx, p.SessionID, p.ModeID); err != nil {
		return api.SessionSetModeResult{}, internalErr(err)
	}
	sess.SetCurrentMode(p.ModeID)
	s.norm.PublishModeUpdated(string(p.SessionID), p.ModeID)
	return api.SessionSetModeResult{SessionID: p.SessionID, CurrentModeID: p.ModeID}, nil
}

// SetModel sets a session's active model on its provider and records it, emitting
// agent_model_updated. Validation mirrors SetMode.
func (s *Service) SetModel(ctx context.Context, p api.SessionSetModelParams) (api.SessionSetModelResult, error) {
	sess, ok := s.sessions.Get(p.SessionID)
	if !ok {
		return api.SessionSetModelResult{}, unknownSession(p.SessionID)
	}
	if p.ModelID == "" {
		return api.SessionSetModelResult{}, invalidParams("model_id is required")
	}
	_, available := sess.Models()
	if !hasID(available, p.ModelID, func(m api.SessionModel) string { return m.ID }) {
		return api.SessionSetModelResult{}, invalidParams("session " + string(p.SessionID) + " has no model " + p.ModelID)
	}
	if err := s.manager.SetModel(ctx, p.SessionID, p.ModelID); err != nil {
		return api.SessionSetModelResult{}, internalErr(err)
	}
	sess.SetCurrentModel(p.ModelID)
	s.norm.PublishModelUpdated(string(p.SessionID), p.ModelID)
	return api.SessionSetModelResult{SessionID: p.SessionID, CurrentModelID: p.ModelID}, nil
}

// SetThoughtLevel sets a session's active reasoning/thought level on its provider
// and records it, emitting agent_thought_level_updated. Validation mirrors
// SetMode; thought levels come from the provider's configOptions.
func (s *Service) SetThoughtLevel(ctx context.Context, p api.SessionSetThoughtLevelParams) (api.SessionSetThoughtLevelResult, error) {
	sess, ok := s.sessions.Get(p.SessionID)
	if !ok {
		return api.SessionSetThoughtLevelResult{}, unknownSession(p.SessionID)
	}
	if p.ThoughtLevelID == "" {
		return api.SessionSetThoughtLevelResult{}, invalidParams("thought_level_id is required")
	}
	_, available := sess.ThoughtLevels()
	if !hasID(available, p.ThoughtLevelID, func(t api.SessionThoughtLevel) string { return t.ID }) {
		return api.SessionSetThoughtLevelResult{}, invalidParams("session " + string(p.SessionID) + " has no thought level " + p.ThoughtLevelID)
	}
	if err := s.manager.SetThoughtLevel(ctx, p.SessionID, p.ThoughtLevelID); err != nil {
		return api.SessionSetThoughtLevelResult{}, internalErr(err)
	}
	sess.SetCurrentThoughtLevel(p.ThoughtLevelID)
	s.norm.PublishThoughtLevelUpdated(string(p.SessionID), p.ThoughtLevelID)
	return api.SessionSetThoughtLevelResult{SessionID: p.SessionID, CurrentThoughtLevelID: p.ThoughtLevelID}, nil
}

// hasID reports whether any element's id (via key) equals want.
func hasID[T any](items []T, want string, key func(T) string) bool {
	for _, it := range items {
		if key(it) == want {
			return true
		}
	}
	return false
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
	// Build the ACP prompt content before any state transition, so a malformed
	// request fails without leaving the session running.
	prompt, err := promptBlocks(p)
	if err != nil {
		return api.AgentPromptResult{}, invalidParams(err.Error())
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

	// Record the user's prompt content on the session stream so a replay/resume
	// sees the human side of the conversation (the stream otherwise carries only
	// agent output). Emitted before the send, ahead of the agent's response, and
	// skipped for an empty turn (no text, no attachments) to avoid noise records.
	if up := userPrompt(p.SessionID, p); up.Text != "" || up.Attachments > 0 {
		s.norm.PublishUserPrompt(up)
	}
	// Report the size of the prompt input composed for this turn, before the send
	// (so it is reported even if the send fails). It is the only context the
	// daemon authoritatively knows: the agent owns its system prompt and history.
	s.norm.PublishPromptUsage(measurePromptUsage(p.SessionID, p))

	params := acp.PromptParams{Prompt: prompt}
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

// promptBlocks builds the ACP prompt content for a turn. With no Content it
// preserves the plain-text path (a single text block from Text, even when empty,
// as before); with Content it converts each block to its ACP form, rejecting an
// unknown or incomplete block.
func promptBlocks(p api.AgentPromptParams) ([]json.RawMessage, error) {
	if len(p.Content) == 0 {
		return []json.RawMessage{textBlock(p.Text)}, nil
	}
	blocks := make([]json.RawMessage, 0, len(p.Content))
	for i, c := range p.Content {
		b, err := acpContentBlock(c)
		if err != nil {
			return nil, fmt.Errorf("content[%d]: %w", i, err)
		}
		blocks = append(blocks, b)
	}
	return blocks, nil
}

// acpContentBlock converts one editor content block to its ACP wire form
// (camelCase, as providers expect). Resource links carry a uri; image/audio
// blobs carry a mime type and base64 data.
func acpContentBlock(c api.PromptContentBlock) (json.RawMessage, error) {
	switch c.Type {
	case api.PromptContentText:
		return textBlock(c.Text), nil
	case api.PromptContentResourceLink:
		if c.URI == "" {
			return nil, fmt.Errorf("%s requires uri", c.Type)
		}
		return json.Marshal(struct {
			Type string `json:"type"`
			URI  string `json:"uri"`
			Name string `json:"name,omitempty"`
		}{Type: c.Type, URI: c.URI, Name: c.Name})
	case api.PromptContentImage, api.PromptContentAudio:
		if c.MimeType == "" || c.Data == "" {
			return nil, fmt.Errorf("%s requires mime_type and data", c.Type)
		}
		return json.Marshal(struct {
			Type     string `json:"type"`
			MimeType string `json:"mimeType"`
			URI      string `json:"uri,omitempty"`
			Data     string `json:"data"`
		}{Type: c.Type, MimeType: c.MimeType, URI: c.URI, Data: c.Data})
	default:
		return nil, fmt.Errorf("unknown content type %q", c.Type)
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
