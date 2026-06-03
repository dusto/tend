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
)

// ErrNoSession is returned when a prompt names an unknown session.
var ErrNoSession = errors.New("acp: unknown session")

// NewSessionParams is the session/new request.
type NewSessionParams struct {
	Cwd        string            `json:"cwd"`
	MCPServers []json.RawMessage `json:"mcpServers,omitempty"`
}

// NewSessionResult is the agent's session/new reply.
type NewSessionResult struct {
	SessionID string `json:"sessionId"`
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
type Session struct {
	ID     api.SessionID
	key    Key
	client *Client
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
	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()
	return s, nil
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
