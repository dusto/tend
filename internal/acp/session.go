package acp

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/dusto/tend/api"
)

// ACP session method names.
const (
	MethodNewSession = "session/new"
	MethodPrompt     = "session/prompt"
	MethodCancel     = "session/cancel"
	MethodSetMode    = "session/set_mode"
	MethodSetModel   = "session/set_model"
)

// ErrNoSession is returned when a prompt names an unknown session.
var ErrNoSession = errors.New("acp: unknown session")

// NewSessionParams is the session/new request. mcpServers is a required field
// of the ACP contract (an empty list is valid, but the key must be present);
// strict agents such as claude-agent-acp reject the request when it is absent.
// So it is not omitempty, and a nil slice is normalized to [] before sending
// (see Manager.Open) rather than marshaling as null.
type NewSessionParams struct {
	Cwd        string            `json:"cwd"`
	MCPServers []json.RawMessage `json:"mcpServers"`
}

// NewSessionResult is the agent's session/new reply. modes/models are optional:
// a provider that offers no choice omits them. ACP names the model id "modelId"
// (not "id"), so the two are parsed with distinct shapes.
type NewSessionResult struct {
	SessionID string         `json:"sessionId"`
	Modes     *acpModeState  `json:"modes,omitempty"`
	Models    *acpModelState `json:"models,omitempty"`
}

// acpModeState mirrors the ACP SessionModeState carried by session/new.
type acpModeState struct {
	CurrentModeID  string    `json:"currentModeId"`
	AvailableModes []acpMode `json:"availableModes"`
}

type acpMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// acpModelState mirrors the ACP SessionModelState carried by session/new.
type acpModelState struct {
	CurrentModelID  string     `json:"currentModelId"`
	AvailableModels []acpModel `json:"availableModels"`
}

type acpModel struct {
	ModelID     string `json:"modelId"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// SetModeParams is the session/set_mode request.
type SetModeParams struct {
	SessionID string `json:"sessionId"`
	ModeID    string `json:"modeId"`
}

// SetModelParams is the session/set_model request.
type SetModelParams struct {
	SessionID string `json:"sessionId"`
	ModelID   string `json:"modelId"`
}

// PromptParams is the session/prompt request. SessionID is set by the manager.
type PromptParams struct {
	SessionID string            `json:"sessionId"`
	Prompt    []json.RawMessage `json:"prompt"`
}

// PromptResult is the agent's session/prompt reply.
type PromptResult struct {
	StopReason string `json:"stopReason"`
}

// CancelParams is the session/cancel notification.
type CancelParams struct {
	SessionID string `json:"sessionId"`
}

// Session is a task-scoped ACP session pinned to the process it was created on.
// The mode/model fields capture the provider's advertised choices from
// session/new (empty when it offers none), in TEND wire form for the caller to
// record on the session registry.
type Session struct {
	ID     api.SessionID
	key    Key
	client *Client

	CurrentModeID   string
	AvailableModes  []api.SessionMode
	CurrentModelID  string
	AvailableModels []api.SessionModel
}

// Manager creates task-scoped ACP sessions on pooled processes and routes
// prompts to the process each session lives on. A process may host many
// sessions; turns on a process are serialized through the pool, so only one is
// in flight at a time. A Manager is safe for concurrent use.
type Manager struct {
	pool *Pool

	mu       sync.Mutex
	sessions map[api.SessionID]*Session
}

// NewManager returns a Manager backed by pool.
func NewManager(pool *Pool) *Manager {
	return &Manager{pool: pool, sessions: make(map[api.SessionID]*Session)}
}

// Open creates a session on a process for key: it takes a process from the pool
// (reusing an idle one or spawning), runs session/new on it, pins the session to
// that process, and registers it so the process is not evicted while it hosts
// sessions.
func (m *Manager) Open(ctx context.Context, key Key, params NewSessionParams) (*Session, error) {
	lease, err := m.pool.Acquire(ctx, key, "")
	if err != nil {
		return nil, err
	}
	client, ok := lease.Process().(*Client)
	if !ok {
		lease.Release()
		return nil, errors.New("acp: pooled process is not an *acp.Client")
	}

	// mcpServers is required and must be an array, not null: send [] when none
	// are configured.
	if params.MCPServers == nil {
		params.MCPServers = []json.RawMessage{}
	}
	var res NewSessionResult
	if err := client.Call(ctx, MethodNewSession, params, &res); err != nil {
		lease.Release()
		return nil, err
	}
	// Mark the process as session-hosting before releasing the turn so eviction
	// cannot reap it in the gap.
	m.pool.AddSession(key, client)
	lease.Release()

	s := &Session{ID: api.SessionID(res.SessionID), key: key, client: client}
	if res.Modes != nil {
		s.CurrentModeID = res.Modes.CurrentModeID
		s.AvailableModes = toAPIModes(res.Modes.AvailableModes)
	}
	if res.Models != nil {
		s.CurrentModelID = res.Models.CurrentModelID
		s.AvailableModels = toAPIModels(res.Models.AvailableModels)
	}
	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()
	return s, nil
}

// toAPIModes converts ACP modes to the TEND wire shape.
func toAPIModes(in []acpMode) []api.SessionMode {
	if len(in) == 0 {
		return nil
	}
	out := make([]api.SessionMode, len(in))
	for i, m := range in {
		out[i] = api.SessionMode{ID: m.ID, Name: m.Name, Description: m.Description}
	}
	return out
}

// toAPIModels converts ACP models to the TEND wire shape (ACP's modelId becomes
// the generic id).
func toAPIModels(in []acpModel) []api.SessionModel {
	if len(in) == 0 {
		return nil
	}
	out := make([]api.SessionModel, len(in))
	for i, m := range in {
		out[i] = api.SessionModel{ID: m.ModelID, Name: m.Name, Description: m.Description}
	}
	return out
}

// Prompt routes a prompt to its session's process, runs it as one turn (waiting
// if that process is mid-turn for another session), and returns the result.
func (m *Manager) Prompt(ctx context.Context, sessionID api.SessionID, params PromptParams) (PromptResult, error) {
	m.mu.Lock()
	s := m.sessions[sessionID]
	m.mu.Unlock()
	if s == nil {
		return PromptResult{}, ErrNoSession
	}

	lease, err := m.pool.AcquireOn(ctx, s.key, s.client, sessionID)
	if err != nil {
		return PromptResult{}, err
	}
	defer lease.Release()

	params.SessionID = string(sessionID)
	var res PromptResult
	if err := s.client.Call(ctx, MethodPrompt, params, &res); err != nil {
		return PromptResult{}, err
	}
	return res, nil
}

// SetMode sets a session's active mode on its process via session/set_mode. Like
// Prompt it routes to the session's process and serializes through the pool.
func (m *Manager) SetMode(ctx context.Context, sessionID api.SessionID, modeID string) error {
	return m.callOnSession(ctx, sessionID, MethodSetMode, SetModeParams{SessionID: string(sessionID), ModeID: modeID})
}

// SetModel sets a session's active model on its process via session/set_model.
func (m *Manager) SetModel(ctx context.Context, sessionID api.SessionID, modelID string) error {
	return m.callOnSession(ctx, sessionID, MethodSetModel, SetModelParams{SessionID: string(sessionID), ModelID: modelID})
}

// callOnSession runs a request on the session's process, acquiring the process
// turn so it does not race an in-flight prompt. The result is discarded; these
// ACP methods reply with null on success.
func (m *Manager) callOnSession(ctx context.Context, sessionID api.SessionID, method string, params any) error {
	m.mu.Lock()
	s := m.sessions[sessionID]
	m.mu.Unlock()
	if s == nil {
		return ErrNoSession
	}
	lease, err := m.pool.AcquireOn(ctx, s.key, s.client, sessionID)
	if err != nil {
		return err
	}
	defer lease.Release()
	return s.client.Call(ctx, method, params, nil)
}

// Cancel asks the agent to abort the in-flight turn on a session by sending the
// session/cancel notification to its process. It is a best-effort signal: the
// turn ends when the agent acknowledges it by returning from session/prompt.
func (m *Manager) Cancel(ctx context.Context, sessionID api.SessionID) error {
	m.mu.Lock()
	s := m.sessions[sessionID]
	m.mu.Unlock()
	if s == nil {
		return ErrNoSession
	}
	return s.client.Notify(ctx, MethodCancel, CancelParams{SessionID: string(sessionID)})
}

// Close drops a session, releasing its hold on the hosting process so the
// process becomes eligible for idle eviction once it hosts no sessions. It does
// not stop the process (other sessions may share it).
func (m *Manager) Close(sessionID api.SessionID) {
	m.mu.Lock()
	s := m.sessions[sessionID]
	delete(m.sessions, sessionID)
	m.mu.Unlock()
	if s != nil {
		m.pool.RemoveSession(s.key, s.client)
	}
}
