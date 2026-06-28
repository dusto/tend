package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/rpc"
)

// fakeAgent is an in-process ACP agent: it answers session/new with incrementing
// ids and session/prompt with end_turn, recording the prompts it receives and
// optionally gating them to exercise turn serialization.
type fakeAgent struct {
	mu       sync.Mutex
	nextSess int
	prompts  []string        // session ids of prompts the agent handled
	lastNew  json.RawMessage // raw params of the most recent session/new

	started chan string   // if set, receives a session id when a prompt handler starts
	gate    chan struct{} // if set, prompt handlers block on it

	// modes/models returned in the session/new reply (nil = provider offers none).
	newModes  *acpModeState
	newModels *acpModelState
	// set_mode/set_model the agent received.
	lastSetMode  json.RawMessage
	lastSetModel json.RawMessage
}

func (a *fakeAgent) handle(_ context.Context, req *rpc.Request) (any, error) {
	switch req.Method {
	case MethodNewSession:
		a.mu.Lock()
		a.lastNew = append(json.RawMessage(nil), req.Params...)
		a.nextSess++
		id := fmt.Sprintf("sess-%d", a.nextSess)
		res := NewSessionResult{SessionID: id, Modes: a.newModes, Models: a.newModels}
		a.mu.Unlock()
		return res, nil
	case MethodPrompt:
		var p PromptParams
		_ = json.Unmarshal(req.Params, &p)
		if a.started != nil {
			a.started <- p.SessionID
		}
		if a.gate != nil {
			<-a.gate
		}
		a.mu.Lock()
		a.prompts = append(a.prompts, p.SessionID)
		a.mu.Unlock()
		return PromptResult{StopReason: "end_turn"}, nil
	case MethodSetMode:
		a.mu.Lock()
		a.lastSetMode = append(json.RawMessage(nil), req.Params...)
		a.mu.Unlock()
		return nil, nil
	case MethodSetModel:
		a.mu.Lock()
		a.lastSetModel = append(json.RawMessage(nil), req.Params...)
		a.mu.Unlock()
		return nil, nil
	}
	return nil, &rpc.Error{Code: rpc.CodeMethodNotFound, Message: req.Method}
}

func (a *fakeAgent) gotPrompts() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.prompts...)
}

// agentSpawner returns a SpawnFunc that wires each spawned process to agent.
func agentSpawner(t *testing.T, agent *fakeAgent) SpawnFunc {
	t.Helper()
	return func(_ context.Context, _ Key) (Process, error) {
		a, b := net.Pipe()
		agentConn := rpc.NewConn(a, rpc.HandlerFunc(agent.handle))
		t.Cleanup(func() { _ = agentConn.Close() })
		return newClient(b, nil), nil
	}
}

func newManager(t *testing.T, agent *fakeAgent, opts Options) *Manager {
	t.Helper()
	p := NewPool(agentSpawner(t, agent), nil, opts)
	t.Cleanup(func() { _ = p.Close() })
	return NewManager(p)
}

func openSession(t *testing.T, m *Manager, key Key) *Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s, err := m.Open(ctx, key, NewSessionParams{Cwd: "/repo"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func prompt(t *testing.T, m *Manager, id api.SessionID) PromptResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := m.Prompt(ctx, id, PromptParams{Prompt: []json.RawMessage{json.RawMessage(`{"type":"text","text":"hi"}`)}})
	if err != nil {
		t.Fatalf("Prompt %s: %v", id, err)
	}
	return res
}

func TestOpenCreatesSession(t *testing.T) {
	m := newManager(t, &fakeAgent{}, Options{Max: 2})
	s := openSession(t, m, testKey)
	if s.ID != "sess-1" {
		t.Errorf("session ID = %q, want sess-1", s.ID)
	}
}

// TestNewSessionAlwaysSendsMCPServers pins the wire shape: mcpServers is a
// required ACP field, so even with none configured the request must carry it as
// an empty array (not null, not absent). A real agent (claude-agent-acp) rejects
// the missing key with Invalid params.
func TestNewSessionAlwaysSendsMCPServers(t *testing.T) {
	agent := &fakeAgent{}
	m := newManager(t, agent, Options{Max: 1})
	openSession(t, m, testKey) // openSession passes no MCPServers

	agent.mu.Lock()
	raw := agent.lastNew
	agent.mu.Unlock()

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("session/new params: %v", err)
	}
	v, ok := fields["mcpServers"]
	if !ok {
		t.Fatalf("session/new is missing the required mcpServers key: %s", raw)
	}
	if string(v) != "[]" {
		t.Errorf("mcpServers = %s, want [] (not null/absent)", v)
	}
}

func TestPromptRoutesToCorrectSession(t *testing.T) {
	agent := &fakeAgent{}
	m := newManager(t, agent, Options{Max: 2})

	s1 := openSession(t, m, testKey)
	s2 := openSession(t, m, testKey)

	if res := prompt(t, m, s1.ID); res.StopReason != "end_turn" {
		t.Errorf("StopReason = %q", res.StopReason)
	}
	prompt(t, m, s2.ID)
	prompt(t, m, s1.ID)

	// The agent received each prompt under the routed session's id, in order.
	want := []string{string(s1.ID), string(s2.ID), string(s1.ID)}
	if got := agent.gotPrompts(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("agent prompts = %v, want %v", got, want)
	}
}

func TestMultipleSessionsPerProcess(t *testing.T) {
	m := newManager(t, &fakeAgent{}, Options{Max: 1}) // one process for the key

	s1 := openSession(t, m, testKey)
	s2 := openSession(t, m, testKey)
	if s1.ID == s2.ID {
		t.Fatal("sessions share an id")
	}
	if s1.client != s2.client {
		t.Error("sessions were not hosted on the same (only) process")
	}
}

func TestPromptUnknownSession(t *testing.T) {
	m := newManager(t, &fakeAgent{}, Options{Max: 1})
	if _, err := m.Prompt(context.Background(), "nope", PromptParams{}); !errors.Is(err, ErrNoSession) {
		t.Fatalf("Prompt unknown = %v, want ErrNoSession", err)
	}
}

// TestOneActiveTurnPerProcess verifies that two sessions sharing a process do
// not run turns concurrently: the second prompt's handler must not start until
// the first turn is released.
func TestOneActiveTurnPerProcess(t *testing.T) {
	agent := &fakeAgent{started: make(chan string, 2), gate: make(chan struct{})}
	m := newManager(t, agent, Options{Max: 1})

	s1 := openSession(t, m, testKey)
	s2 := openSession(t, m, testKey)

	done1 := make(chan struct{})
	go func() {
		prompt(t, m, s1.ID)
		close(done1)
	}()

	// First turn's handler is running and gated.
	if got := <-agent.started; got != string(s1.ID) {
		t.Fatalf("first started = %q, want %s", got, s1.ID)
	}

	done2 := make(chan struct{})
	go func() {
		prompt(t, m, s2.ID)
		close(done2)
	}()

	// While the first turn holds the process, the second handler must not start.
	select {
	case got := <-agent.started:
		t.Fatalf("second turn started (%q) while the first held the process", got)
	case <-time.After(150 * time.Millisecond):
	}

	close(agent.gate) // release both turns
	<-done1
	<-done2
	if got := <-agent.started; got != string(s2.ID) {
		t.Errorf("second started = %q, want %s", got, s2.ID)
	}
}

func TestSessionHostingBlocksEviction(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	m := newManager(t, &fakeAgent{}, Options{Max: 1, IdleTTL: time.Minute, Now: clock.now})

	s := openSession(t, m, testKey)
	client := s.client

	clock.advance(2 * time.Minute)
	m.pool.evictIdle()
	select {
	case <-client.Done():
		t.Fatal("evicted a process that still hosts a session")
	case <-time.After(100 * time.Millisecond):
	}

	// Once the session closes, the now-idle process is evictable.
	m.Close(s.ID)
	clock.advance(2 * time.Minute)
	m.pool.evictIdle()
	select {
	case <-client.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("process not evicted after its last session closed")
	}
}

func TestOpenCapturesModesAndModels(t *testing.T) {
	agent := &fakeAgent{
		newModes: &acpModeState{
			CurrentModeID: "default",
			AvailableModes: []acpMode{
				{ID: "default", Name: "Default"},
				{ID: "think", Name: "Think harder", Description: "more reasoning"},
			},
		},
		newModels: &acpModelState{
			CurrentModelID: "sonnet",
			AvailableModels: []acpModel{
				{ModelID: "sonnet", Name: "Sonnet"},
				{ModelID: "opus", Name: "Opus", Description: "most capable"},
			},
		},
	}
	m := newManager(t, agent, Options{Max: 1})
	s := openSession(t, m, testKey)

	if s.CurrentModeID != "default" {
		t.Errorf("CurrentModeID = %q, want default", s.CurrentModeID)
	}
	if len(s.AvailableModes) != 2 || s.AvailableModes[1].ID != "think" || s.AvailableModes[1].Description != "more reasoning" {
		t.Errorf("AvailableModes = %+v", s.AvailableModes)
	}
	if s.CurrentModelID != "sonnet" {
		t.Errorf("CurrentModelID = %q, want sonnet", s.CurrentModelID)
	}
	// ACP's modelId maps onto the generic id.
	if len(s.AvailableModels) != 2 || s.AvailableModels[1].ID != "opus" || s.AvailableModels[1].Name != "Opus" {
		t.Errorf("AvailableModels = %+v", s.AvailableModels)
	}
}

func TestOpenWithoutModesOrModels(t *testing.T) {
	m := newManager(t, &fakeAgent{}, Options{Max: 1})
	s := openSession(t, m, testKey)
	if s.CurrentModeID != "" || s.AvailableModes != nil || s.CurrentModelID != "" || s.AvailableModels != nil {
		t.Errorf("expected empty mode/model state, got mode %q/%v model %q/%v",
			s.CurrentModeID, s.AvailableModes, s.CurrentModelID, s.AvailableModels)
	}
}

func TestSetModeAndSetModelRouteToSession(t *testing.T) {
	agent := &fakeAgent{}
	m := newManager(t, agent, Options{Max: 1})
	s := openSession(t, m, testKey)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.SetMode(ctx, s.ID, "think"); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	if err := m.SetModel(ctx, s.ID, "opus"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}

	agent.mu.Lock()
	gotMode, gotModel := agent.lastSetMode, agent.lastSetModel
	agent.mu.Unlock()

	var mode SetModeParams
	if err := json.Unmarshal(gotMode, &mode); err != nil {
		t.Fatalf("set_mode params: %v", err)
	}
	if mode.SessionID != string(s.ID) || mode.ModeID != "think" {
		t.Errorf("set_mode = %+v, want sessionId=%s modeId=think", mode, s.ID)
	}
	var model SetModelParams
	if err := json.Unmarshal(gotModel, &model); err != nil {
		t.Fatalf("set_model params: %v", err)
	}
	if model.SessionID != string(s.ID) || model.ModelID != "opus" {
		t.Errorf("set_model = %+v, want sessionId=%s modelId=opus", model, s.ID)
	}
}

func TestSetModeUnknownSession(t *testing.T) {
	m := newManager(t, &fakeAgent{}, Options{Max: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.SetMode(ctx, "nope", "x"); !errors.Is(err, ErrNoSession) {
		t.Errorf("SetMode unknown session err = %v, want ErrNoSession", err)
	}
}
