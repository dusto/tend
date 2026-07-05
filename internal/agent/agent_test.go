package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/acp"
	"github.com/dusto/tend/internal/events"
	"github.com/dusto/tend/internal/session"
)

// fakeManager stands in for *acp.Manager: it records calls and lets a test drive
// what Open/Prompt return, including a prompt that blocks until its ctx is done.
type fakeManager struct {
	mu sync.Mutex

	openID       api.SessionID
	openErr      error
	openParams   acp.NewSessionParams
	openSpawnDir string // worktree root carried on the Open ctx
	// mode/model/thought state returned from Open (as captured from session/new).
	openModeCurrent         string
	openModes               []api.SessionMode
	openModelCurrent        string
	openModels              []api.SessionModel
	openThoughtLevelCurrent string
	openThoughtLevels       []api.SessionThoughtLevel

	setModeErr         error
	setModelErr        error
	setThoughtLevelErr error
	setModeID          string // last mode id passed to SetMode
	setModelID         string // last model id passed to SetModel
	setThoughtLevelID  string // last thought level id passed to SetThoughtLevel

	promptCalls  int              // number of Prompt invocations
	promptParams acp.PromptParams // params from the last Prompt call

	promptResult acp.PromptResult
	promptErr    error
	promptBlock  bool          // if set, Prompt blocks until ctx is done
	promptStart  chan struct{} // closed once Prompt has been entered

	cancelled []api.SessionID
	closed    []api.SessionID
}

func (m *fakeManager) Open(ctx context.Context, _ acp.Key, params acp.NewSessionParams) (*acp.Session, error) {
	m.mu.Lock()
	m.openParams = params
	m.openSpawnDir = acp.WorktreeRootFromContext(ctx)
	m.mu.Unlock()
	if m.openErr != nil {
		return nil, m.openErr
	}
	return &acp.Session{
		ID:                     m.openID,
		CurrentModeID:          m.openModeCurrent,
		AvailableModes:         m.openModes,
		CurrentModelID:         m.openModelCurrent,
		AvailableModels:        m.openModels,
		CurrentThoughtLevelID:  m.openThoughtLevelCurrent,
		AvailableThoughtLevels: m.openThoughtLevels,
	}, nil
}

func (m *fakeManager) Prompt(ctx context.Context, _ api.SessionID, params acp.PromptParams) (acp.PromptResult, error) {
	m.mu.Lock()
	m.promptCalls++
	m.promptParams = params
	block := m.promptBlock
	res, errResult := m.promptResult, m.promptErr
	m.mu.Unlock()
	if m.promptStart != nil {
		close(m.promptStart)
	}
	if block {
		<-ctx.Done()
		return acp.PromptResult{}, ctx.Err()
	}
	return res, errResult
}

func (m *fakeManager) Cancel(_ context.Context, id api.SessionID) error {
	m.mu.Lock()
	m.cancelled = append(m.cancelled, id)
	m.mu.Unlock()
	return nil
}

func (m *fakeManager) Close(id api.SessionID) {
	m.mu.Lock()
	m.closed = append(m.closed, id)
	m.mu.Unlock()
}

func (m *fakeManager) SetMode(_ context.Context, _ api.SessionID, modeID string) error {
	m.mu.Lock()
	m.setModeID = modeID
	err := m.setModeErr
	m.mu.Unlock()
	return err
}

func (m *fakeManager) SetModel(_ context.Context, _ api.SessionID, modelID string) error {
	m.mu.Lock()
	m.setModelID = modelID
	err := m.setModelErr
	m.mu.Unlock()
	return err
}

func (m *fakeManager) SetThoughtLevel(_ context.Context, _ api.SessionID, thoughtLevelID string) error {
	m.mu.Lock()
	m.setThoughtLevelID = thoughtLevelID
	err := m.setThoughtLevelErr
	m.mu.Unlock()
	return err
}

func (m *fakeManager) cancels() []api.SessionID {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]api.SessionID(nil), m.cancelled...)
}

func newService(t *testing.T, mgr Manager) (*Service, *events.Store) {
	t.Helper()
	log, err := events.OpenLog(filepath.Join(t.TempDir(), "events.log"))
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	store := events.NewStore(log)
	norm := acp.NewNormalizer(store, nil)
	return NewService(session.NewRegistry(), mgr, norm), store
}

func TestStartRegistersSession(t *testing.T) {
	mgr := &fakeManager{openID: "sess-1"}
	svc, _ := newService(t, mgr)

	task := api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"}
	res, err := svc.Start(context.Background(), api.AgentStartParams{
		ProviderID:   "codex",
		Task:         task,
		WorktreeRoot: "/repo/wt",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if res.SessionID != "sess-1" || res.StreamID != "session:sess-1" || res.Status != api.StatusIdle {
		t.Fatalf("result = %+v", res)
	}
	// The worktree root is passed to the ACP session as its cwd, and carried on
	// the spawn ctx so a freshly spawned provider process starts in it.
	if mgr.openParams.Cwd != "/repo/wt" {
		t.Errorf("session cwd = %q, want /repo/wt", mgr.openParams.Cwd)
	}
	if mgr.openSpawnDir != "/repo/wt" {
		t.Errorf("spawn worktree root = %q, want /repo/wt", mgr.openSpawnDir)
	}
	if s, ok := svc.sessions.Get("sess-1"); !ok || s.Task != task || s.ProviderID != "codex" {
		t.Errorf("registered session = %+v, ok=%v", s, ok)
	}
}

func TestStartTaskless(t *testing.T) {
	mgr := &fakeManager{openID: "sess-1"}
	svc, _ := newService(t, mgr)

	// No task, explicit workspace: a conversation session.
	res, err := svc.Start(context.Background(), api.AgentStartParams{
		ProviderID:   "codex",
		WorkspaceID:  "ws1",
		WorktreeRoot: "/repo/wt",
	})
	if err != nil {
		t.Fatalf("task-less Start: %v", err)
	}
	s, ok := svc.sessions.Get(res.SessionID)
	if !ok {
		t.Fatalf("session not registered")
	}
	if s.HasTask() {
		t.Errorf("session should be task-less, got task %+v", s.Task)
	}
	if s.WorkspaceID != "ws1" {
		t.Errorf("workspace = %q, want ws1", s.WorkspaceID)
	}
}

func TestStartRequiresWorkspaceOrTask(t *testing.T) {
	svc, _ := newService(t, &fakeManager{openID: "x"})
	// Provider + worktree but no workspace and no task: unresolvable workspace.
	_, err := svc.Start(context.Background(), api.AgentStartParams{
		ProviderID:   "codex",
		WorktreeRoot: "/r",
	})
	if err == nil {
		t.Error("expected an error when neither workspace_id nor task supplies the workspace")
	}
}

func TestStartRejectsConflictingWorkspace(t *testing.T) {
	svc, _ := newService(t, &fakeManager{openID: "x"})
	// Both an explicit workspace and a task workspace, but they disagree: the
	// session would be bound to one workspace while the task authorizes another.
	_, err := svc.Start(context.Background(), api.AgentStartParams{
		ProviderID:   "codex",
		WorktreeRoot: "/r",
		WorkspaceID:  "ws-a",
		Task:         api.TaskRef{Provider: "beads", WorkspaceID: "ws-b", ID: "t1"},
	})
	if err == nil {
		t.Error("expected an error when workspace_id and task.workspace_id disagree")
	}
}

func TestStartValidatesParams(t *testing.T) {
	svc, _ := newService(t, &fakeManager{openID: "x"})
	full := api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"}
	cases := []api.AgentStartParams{
		{Task: full, WorktreeRoot: "/r"},  // no provider
		{ProviderID: "codex", Task: full}, // no worktree
		{ProviderID: "codex", WorktreeRoot: "/r", Task: api.TaskRef{WorkspaceID: "ws1", ID: "t1"}},          // no task.provider
		{ProviderID: "codex", WorktreeRoot: "/r", Task: api.TaskRef{Provider: "beads", ID: "t1"}},           // no task.workspace_id
		{ProviderID: "codex", WorktreeRoot: "/r", Task: api.TaskRef{Provider: "beads", WorkspaceID: "ws1"}}, // no task.id
	}
	for i, p := range cases {
		if _, err := svc.Start(context.Background(), p); err == nil {
			t.Errorf("case %d: expected validation error", i)
		}
	}
}

func TestStartOpenFailure(t *testing.T) {
	mgr := &fakeManager{openErr: errors.New("spawn failed")}
	svc, _ := newService(t, mgr)
	_, err := svc.Start(context.Background(), api.AgentStartParams{
		ProviderID: "codex", Task: api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"}, WorktreeRoot: "/r",
	})
	if err == nil {
		t.Fatal("expected error when Open fails")
	}
}

func TestPromptCompletesTurn(t *testing.T) {
	mgr := &fakeManager{openID: "sess-1", promptResult: acp.PromptResult{StopReason: "end_turn"}}
	svc, store := newService(t, mgr)
	startSession(t, svc)

	res, err := svc.Prompt(context.Background(), api.AgentPromptParams{SessionID: "sess-1", Text: "hi"})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if res.StopReason != "end_turn" || res.Status != api.StatusIdle {
		t.Fatalf("result = %+v", res)
	}
	if s, _ := svc.sessions.Get("sess-1"); s.Status() != api.StatusIdle {
		t.Errorf("status = %q, want idle", s.Status())
	}
	// A turn publishes prompt usage (before the send) then turn_end (on completion).
	evs, _, err := store.Read("session:sess-1", 0, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(evs) != 2 || evs[0].Type != "agent_prompt_usage" || evs[1].Type != "turn_end" {
		t.Fatalf("events = %+v, want agent_prompt_usage then turn_end", evs)
	}
}

func TestPromptEmitsUsage(t *testing.T) {
	mgr := &fakeManager{openID: "sess-1", promptResult: acp.PromptResult{StopReason: "end_turn"}}
	svc, store := newService(t, mgr)
	startSession(t, svc)

	if _, err := svc.Prompt(context.Background(), api.AgentPromptParams{SessionID: "sess-1", Text: "hello"}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	evs, _, err := store.Read("session:sess-1", 0, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var usage api.AgentPromptUsage
	if err := json.Unmarshal(evs[0].Payload, &usage); err != nil {
		t.Fatalf("unmarshal usage: %v", err)
	}
	if usage.SessionID != "sess-1" || usage.TextChars != 5 || usage.TokensApprox != 2 || !usage.Approximate {
		t.Errorf("usage = %+v, want sess-1/5 chars/2 tokens/approximate", usage)
	}
}

func TestPromptUnknownSession(t *testing.T) {
	svc, _ := newService(t, &fakeManager{})
	if _, err := svc.Prompt(context.Background(), api.AgentPromptParams{SessionID: "nope"}); err == nil {
		t.Error("expected error for unknown session")
	}
}

// decodeBlocks unmarshals the ACP prompt content the manager received.
func decodeBlocks(t *testing.T, params acp.PromptParams) []map[string]any {
	t.Helper()
	out := make([]map[string]any, len(params.Prompt))
	for i, raw := range params.Prompt {
		if err := json.Unmarshal(raw, &out[i]); err != nil {
			t.Fatalf("block %d: %v", i, err)
		}
	}
	return out
}

func TestPromptTextOnlyBuildsSingleTextBlock(t *testing.T) {
	mgr := &fakeManager{openID: "sess-1", promptResult: acp.PromptResult{StopReason: "end_turn"}}
	svc, _ := newService(t, mgr)
	startSession(t, svc)

	if _, err := svc.Prompt(context.Background(), api.AgentPromptParams{SessionID: "sess-1", Text: "hi"}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	blocks := decodeBlocks(t, mgr.promptParams)
	if len(blocks) != 1 || blocks[0]["type"] != "text" || blocks[0]["text"] != "hi" {
		t.Fatalf("blocks = %+v, want one text block", blocks)
	}
}

func TestPromptContentBlocksForwardedAsACP(t *testing.T) {
	mgr := &fakeManager{openID: "sess-1", promptResult: acp.PromptResult{StopReason: "end_turn"}}
	svc, _ := newService(t, mgr)
	startSession(t, svc)

	_, err := svc.Prompt(context.Background(), api.AgentPromptParams{
		SessionID: "sess-1",
		Text:      "ignored when content is set",
		Content: []api.PromptContentBlock{
			{Type: api.PromptContentText, Text: "look at this"},
			{Type: api.PromptContentResourceLink, URI: "file:///r/main.go", Name: "main.go"},
			{Type: api.PromptContentImage, MimeType: "image/png", Data: "AAAA"},
		},
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	blocks := decodeBlocks(t, mgr.promptParams)
	if len(blocks) != 3 {
		t.Fatalf("blocks = %+v, want 3", blocks)
	}
	if blocks[0]["type"] != "text" || blocks[0]["text"] != "look at this" {
		t.Errorf("block 0 = %+v", blocks[0])
	}
	// Resource link: uri + name, no text.
	if blocks[1]["type"] != "resource_link" || blocks[1]["uri"] != "file:///r/main.go" || blocks[1]["name"] != "main.go" {
		t.Errorf("block 1 = %+v", blocks[1])
	}
	// Image: ACP camelCase mimeType + base64 data.
	if blocks[2]["type"] != "image" || blocks[2]["mimeType"] != "image/png" || blocks[2]["data"] != "AAAA" {
		t.Errorf("block 2 = %+v", blocks[2])
	}
}

func TestPromptRejectsBadContentWithoutRunning(t *testing.T) {
	cases := []api.PromptContentBlock{
		{Type: "bogus"},                                       // unknown type
		{Type: api.PromptContentResourceLink},                 // missing uri
		{Type: api.PromptContentImage, MimeType: "image/png"}, // missing data
	}
	for i, block := range cases {
		mgr := &fakeManager{openID: "sess-1"}
		svc, _ := newService(t, mgr)
		startSession(t, svc)

		_, err := svc.Prompt(context.Background(), api.AgentPromptParams{
			SessionID: "sess-1",
			Content:   []api.PromptContentBlock{block},
		})
		if err == nil {
			t.Errorf("case %d: expected validation error", i)
		}
		// A rejected turn never reaches the manager and leaves the session idle.
		if mgr.promptCalls != 0 {
			t.Errorf("case %d: prompt calls = %d, want 0", i, mgr.promptCalls)
		}
		if s, _ := svc.sessions.Get("sess-1"); s.Status() != api.StatusIdle {
			t.Errorf("case %d: status = %q, want idle", i, s.Status())
		}
	}
}

func TestPromptFailureMarksError(t *testing.T) {
	mgr := &fakeManager{openID: "sess-1", promptErr: errors.New("boom")}
	svc, store := newService(t, mgr)
	startSession(t, svc)

	if _, err := svc.Prompt(context.Background(), api.AgentPromptParams{SessionID: "sess-1"}); err == nil {
		t.Fatal("expected error from failed turn")
	}
	if s, _ := svc.sessions.Get("sess-1"); s.Status() != api.StatusError {
		t.Errorf("status = %q, want error", s.Status())
	}
	// The prompt input is still measured (we sent it before the turn failed), but
	// a failed turn publishes no turn_end: only the usage event is on the stream.
	evs, _, err := store.Read("session:sess-1", 0, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(evs) != 1 || evs[0].Type != "agent_prompt_usage" {
		t.Errorf("events = %+v, want only agent_prompt_usage (no turn_end on failure)", evs)
	}
}

func TestPromptRetriesAfterError(t *testing.T) {
	mgr := &fakeManager{openID: "sess-1", promptErr: errors.New("boom")}
	svc, _ := newService(t, mgr)
	startSession(t, svc)

	if _, err := svc.Prompt(context.Background(), api.AgentPromptParams{SessionID: "sess-1"}); err == nil {
		t.Fatal("expected first turn to fail")
	}
	// Recover: the next turn succeeds, taking the errored session back through
	// idle to running and on to idle.
	mgr.mu.Lock()
	mgr.promptErr = nil
	mgr.promptResult = acp.PromptResult{StopReason: "end_turn"}
	mgr.mu.Unlock()

	res, err := svc.Prompt(context.Background(), api.AgentPromptParams{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("retry Prompt: %v", err)
	}
	if res.StopReason != "end_turn" || res.Status != api.StatusIdle {
		t.Fatalf("retry result = %+v", res)
	}
	if s, _ := svc.sessions.Get("sess-1"); s.Status() != api.StatusIdle {
		t.Errorf("status = %q, want idle", s.Status())
	}
	mgr.mu.Lock()
	calls := mgr.promptCalls
	mgr.mu.Unlock()
	if calls != 2 {
		t.Errorf("prompt calls = %d, want 2", calls)
	}
}

func TestCancelReturnsTurnToIdle(t *testing.T) {
	started := make(chan struct{})
	mgr := &fakeManager{openID: "sess-1", promptBlock: true, promptStart: started}
	svc, _ := newService(t, mgr)
	startSession(t, svc)

	done := make(chan api.AgentPromptResult, 1)
	go func() {
		res, _ := svc.Prompt(context.Background(), api.AgentPromptParams{SessionID: "sess-1"})
		done <- res
	}()

	<-started // turn is in flight
	if s, _ := svc.sessions.Get("sess-1"); s.Status() != api.StatusRunning {
		t.Fatalf("status = %q, want running", s.Status())
	}
	if _, err := svc.Cancel(context.Background(), api.AgentCancelParams{SessionID: "sess-1"}); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	select {
	case res := <-done:
		if res.StopReason != "cancelled" || res.Status != api.StatusIdle {
			t.Fatalf("cancelled result = %+v", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Prompt did not return after Cancel")
	}
	if got := mgr.cancels(); len(got) != 1 || got[0] != "sess-1" {
		t.Errorf("manager cancels = %v, want [sess-1]", got)
	}
}

func TestStopEndsSession(t *testing.T) {
	mgr := &fakeManager{openID: "sess-1"}
	svc, _ := newService(t, mgr)
	startSession(t, svc)

	if _, err := svc.Stop(context.Background(), api.AgentStopParams{SessionID: "sess-1"}); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, ok := svc.sessions.Get("sess-1"); ok {
		t.Error("session should be removed after Stop")
	}
	mgr.mu.Lock()
	closed := append([]api.SessionID(nil), mgr.closed...)
	mgr.mu.Unlock()
	if len(closed) != 1 || closed[0] != "sess-1" {
		t.Errorf("manager closed = %v, want [sess-1]", closed)
	}
}

func TestStopUnknownSession(t *testing.T) {
	svc, _ := newService(t, &fakeManager{})
	if _, err := svc.Stop(context.Background(), api.AgentStopParams{SessionID: "nope"}); err == nil {
		t.Error("expected error for unknown session")
	}
}

func startSession(t *testing.T, svc *Service) {
	t.Helper()
	if _, err := svc.Start(context.Background(), api.AgentStartParams{
		ProviderID:   "codex",
		Task:         api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"},
		WorktreeRoot: "/repo/wt",
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
}

func TestStartRecordsModesAndModels(t *testing.T) {
	mgr := &fakeManager{
		openID:           "sess-1",
		openModeCurrent:  "default",
		openModes:        []api.SessionMode{{ID: "default", Name: "Default"}, {ID: "think", Name: "Think"}},
		openModelCurrent: "sonnet",
		openModels:       []api.SessionModel{{ID: "sonnet", Name: "Sonnet"}, {ID: "opus", Name: "Opus"}},
	}
	svc, _ := newService(t, mgr)
	startSession(t, svc)

	s, _ := svc.sessions.Get("sess-1")
	curMode, modes := s.Modes()
	if curMode != "default" || len(modes) != 2 || modes[1].ID != "think" {
		t.Errorf("modes = %q %+v", curMode, modes)
	}
	curModel, models := s.Models()
	if curModel != "sonnet" || len(models) != 2 || models[1].ID != "opus" {
		t.Errorf("models = %q %+v", curModel, models)
	}
}

func TestSetModeUpdatesStateAndEmits(t *testing.T) {
	mgr := &fakeManager{
		openID:          "sess-1",
		openModeCurrent: "default",
		openModes:       []api.SessionMode{{ID: "default"}, {ID: "think"}},
	}
	svc, store := newService(t, mgr)
	startSession(t, svc)

	res, err := svc.SetMode(context.Background(), api.SessionSetModeParams{SessionID: "sess-1", ModeID: "think"})
	if err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	if res.CurrentModeID != "think" || res.SessionID != "sess-1" {
		t.Errorf("result = %+v", res)
	}
	if mgr.setModeID != "think" {
		t.Errorf("manager got mode %q, want think", mgr.setModeID)
	}
	if s, _ := svc.sessions.Get("sess-1"); func() string { c, _ := s.Modes(); return c }() != "think" {
		t.Error("session current mode not updated")
	}
	evs, _, _ := store.Read("session:sess-1", 0, 10)
	if len(evs) != 1 || evs[0].Type != "agent_mode_updated" {
		t.Fatalf("events = %+v, want one agent_mode_updated", evs)
	}
}

func TestSetModelUpdatesStateAndEmits(t *testing.T) {
	mgr := &fakeManager{
		openID:           "sess-1",
		openModelCurrent: "sonnet",
		openModels:       []api.SessionModel{{ID: "sonnet"}, {ID: "opus"}},
	}
	svc, store := newService(t, mgr)
	startSession(t, svc)

	res, err := svc.SetModel(context.Background(), api.SessionSetModelParams{SessionID: "sess-1", ModelID: "opus"})
	if err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	if res.CurrentModelID != "opus" {
		t.Errorf("result = %+v", res)
	}
	if mgr.setModelID != "opus" {
		t.Errorf("manager got model %q, want opus", mgr.setModelID)
	}
	evs, _, _ := store.Read("session:sess-1", 0, 10)
	if len(evs) != 1 || evs[0].Type != "agent_model_updated" {
		t.Fatalf("events = %+v, want one agent_model_updated", evs)
	}
}

func TestSetThoughtLevelUpdatesStateAndEmits(t *testing.T) {
	mgr := &fakeManager{
		openID:                  "sess-1",
		openThoughtLevelCurrent: "medium",
		openThoughtLevels:       []api.SessionThoughtLevel{{ID: "medium"}, {ID: "high"}},
	}
	svc, store := newService(t, mgr)
	startSession(t, svc)

	res, err := svc.SetThoughtLevel(context.Background(), api.SessionSetThoughtLevelParams{SessionID: "sess-1", ThoughtLevelID: "high"})
	if err != nil {
		t.Fatalf("SetThoughtLevel: %v", err)
	}
	if res.CurrentThoughtLevelID != "high" || res.SessionID != "sess-1" {
		t.Errorf("result = %+v", res)
	}
	if mgr.setThoughtLevelID != "high" {
		t.Errorf("manager got thought level %q, want high", mgr.setThoughtLevelID)
	}
	if s, _ := svc.sessions.Get("sess-1"); func() string { c, _ := s.ThoughtLevels(); return c }() != "high" {
		t.Error("session current thought level not updated")
	}
	evs, _, _ := store.Read("session:sess-1", 0, 10)
	if len(evs) != 1 || evs[0].Type != "agent_thought_level_updated" {
		t.Fatalf("events = %+v, want one agent_thought_level_updated", evs)
	}
}

func TestSetThoughtLevelRejectsUnavailable(t *testing.T) {
	// A provider that offers no thought levels rejects the set (no silent no-op).
	mgr := &fakeManager{openID: "sess-1"}
	svc, _ := newService(t, mgr)
	startSession(t, svc)
	if _, err := svc.SetThoughtLevel(context.Background(), api.SessionSetThoughtLevelParams{SessionID: "sess-1", ThoughtLevelID: "high"}); err == nil {
		t.Error("expected error for a session with no thought levels")
	}
}

func TestSetModeRejectsBadInput(t *testing.T) {
	cases := []struct {
		name      string
		modes     []api.SessionMode
		sessionID api.SessionID
		modeID    string
	}{
		{"unknown session", []api.SessionMode{{ID: "think"}}, "nope", "think"},
		{"empty mode id", []api.SessionMode{{ID: "think"}}, "sess-1", ""},
		{"mode not advertised", []api.SessionMode{{ID: "default"}}, "sess-1", "think"},
		{"provider offers no modes", nil, "sess-1", "think"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mgr := &fakeManager{openID: "sess-1", openModes: tc.modes}
			svc, store := newService(t, mgr)
			startSession(t, svc)

			_, err := svc.SetMode(context.Background(), api.SessionSetModeParams{SessionID: tc.sessionID, ModeID: tc.modeID})
			if err == nil {
				t.Fatal("expected an error")
			}
			// A rejected set never reaches the provider and emits nothing.
			if mgr.setModeID != "" {
				t.Errorf("manager was called with %q on a rejected set", mgr.setModeID)
			}
			if hw := store.HighWater("session:sess-1"); hw != 0 {
				t.Errorf("high water = %d, want 0 (no event on rejected set)", hw)
			}
		})
	}
}

func TestSetModeManagerErrorLeavesStateUnchanged(t *testing.T) {
	mgr := &fakeManager{
		openID:          "sess-1",
		openModeCurrent: "default",
		openModes:       []api.SessionMode{{ID: "default"}, {ID: "think"}},
		setModeErr:      errors.New("provider refused"),
	}
	svc, store := newService(t, mgr)
	startSession(t, svc)

	if _, err := svc.SetMode(context.Background(), api.SessionSetModeParams{SessionID: "sess-1", ModeID: "think"}); err == nil {
		t.Fatal("expected error from manager")
	}
	// State stays on the prior mode and nothing is emitted.
	if s, _ := svc.sessions.Get("sess-1"); func() string { c, _ := s.Modes(); return c }() != "default" {
		t.Error("current mode changed despite manager error")
	}
	if hw := store.HighWater("session:sess-1"); hw != 0 {
		t.Errorf("high water = %d, want 0", hw)
	}
}
