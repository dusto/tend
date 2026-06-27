package agent

import (
	"context"
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

	promptCalls int // number of Prompt invocations

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
	return &acp.Session{ID: m.openID}, nil
}

func (m *fakeManager) Prompt(ctx context.Context, _ api.SessionID, _ acp.PromptParams) (acp.PromptResult, error) {
	m.mu.Lock()
	m.promptCalls++
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
	// A completed turn publishes turn_end on the session's stream.
	evs, _, err := store.Read("session:sess-1", 0, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(evs) != 1 || evs[0].Type != "turn_end" {
		t.Fatalf("events = %+v, want one turn_end", evs)
	}
}

func TestPromptUnknownSession(t *testing.T) {
	svc, _ := newService(t, &fakeManager{})
	if _, err := svc.Prompt(context.Background(), api.AgentPromptParams{SessionID: "nope"}); err == nil {
		t.Error("expected error for unknown session")
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
	// A failed turn does not publish turn_end.
	if hw := store.HighWater("session:sess-1"); hw != 0 {
		t.Errorf("high water = %d, want 0 (no turn_end on failure)", hw)
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
