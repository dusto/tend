package pty

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/approvals"
	"github.com/dusto/tend/internal/rpc"
	"github.com/dusto/tend/internal/session"
)

type fakeApprover struct {
	outcome approvals.Outcome
	err     error
	kind    string
	detail  json.RawMessage
}

func (a *fakeApprover) Request(_ context.Context, _ *session.Session, kind string, detail json.RawMessage) (approvals.Outcome, error) {
	a.kind = kind
	a.detail = detail
	return a.outcome, a.err
}

// recordingEmitter captures published events for assertions.
type recordingEmitter struct {
	mu     sync.Mutex
	events []api.Event
}

func (e *recordingEmitter) Publish(ev api.Event) (api.Event, error) {
	e.mu.Lock()
	e.events = append(e.events, ev)
	e.mu.Unlock()
	return ev, nil
}

func (e *recordingEmitter) onStream(stream api.StreamID) []api.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []api.Event
	for _, ev := range e.events {
		if ev.StreamID == stream {
			out = append(out, ev)
		}
	}
	return out
}

func newService(t *testing.T, ap approver) (*Service, *session.Registry, *recordingEmitter) {
	t.Helper()
	mgr := NewManager()
	t.Cleanup(mgr.Shutdown)
	reg := session.NewRegistry()
	emit := &recordingEmitter{}
	return NewService(mgr, reg, ap, emit, shell), reg, emit
}

func TestOpenUngated(t *testing.T) {
	ap := &fakeApprover{}
	svc, _, _ := newService(t, ap)

	info, err := svc.open(context.Background(), api.PaneOpenParams{WorkspaceID: "ws1"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if info.PaneID == "" || !info.Running || info.ViewState != api.PaneDetached || info.WorkspaceID != "ws1" {
		t.Fatalf("info = %+v", info)
	}
	if ap.kind != "" {
		t.Error("a user open (no session) must not be gated")
	}
	if len(svc.mgr.List()) != 1 {
		t.Errorf("manager should track the pane")
	}
}

func TestOpenAgentInitiatedApproved(t *testing.T) {
	ap := &fakeApprover{outcome: approvals.Outcome{Approved: true}}
	svc, reg, _ := newService(t, ap)
	cwd := t.TempDir()
	reg.Create("s1", "codex", "ws1", api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"}, cwd)

	info, err := svc.open(context.Background(), api.PaneOpenParams{WorkspaceID: "ws1", Cwd: cwd, SessionID: "s1"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !info.Running {
		t.Fatalf("info = %+v", info)
	}
	if ap.kind != api.ApprovalPaneOpen {
		t.Fatalf("approval kind = %q, want pane_open", ap.kind)
	}
	var detail api.ApprovalDetail
	if err := json.Unmarshal(ap.detail, &detail); err != nil || detail.PaneOpen == nil || detail.PaneOpen.Cwd != cwd {
		t.Errorf("detail = %s, err = %v", ap.detail, err)
	}
}

func TestAgentOpenBindsToSessionWorkspaceAndDefaultCwd(t *testing.T) {
	ap := &fakeApprover{outcome: approvals.Outcome{Approved: true}}
	svc, reg, _ := newService(t, ap)
	worktree := t.TempDir()
	reg.Create("s1", "codex", "ws-session", api.TaskRef{Provider: "beads", WorkspaceID: "ws-session", ID: "t1"}, worktree)

	// Caller supplies a different workspace and no cwd: the pane must bind to the
	// session's workspace/worktree, and cwd defaults to the session worktree.
	info, err := svc.open(context.Background(), api.PaneOpenParams{
		WorkspaceID: "ws-attacker", WorktreeRoot: "/elsewhere", SessionID: "s1",
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if info.WorkspaceID != "ws-session" {
		t.Errorf("pane workspace = %q, want ws-session (from session, not caller)", info.WorkspaceID)
	}
	if info.WorktreeRoot != worktree {
		t.Errorf("pane worktree = %q, want %q", info.WorktreeRoot, worktree)
	}
	var detail api.ApprovalDetail
	_ = json.Unmarshal(ap.detail, &detail)
	if detail.PaneOpen.WorkspaceID != "ws-session" || detail.PaneOpen.Cwd != worktree {
		t.Errorf("approval detail = %+v, want session workspace + worktree cwd", detail.PaneOpen)
	}
}

func TestOpenAgentInitiatedDenied(t *testing.T) {
	ap := &fakeApprover{outcome: approvals.Outcome{Approved: false}}
	svc, reg, _ := newService(t, ap)
	reg.Create("s1", "codex", "ws1", api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"}, "/repo")

	_, err := svc.open(context.Background(), api.PaneOpenParams{WorkspaceID: "ws1", SessionID: "s1"})
	var rpcErr *rpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != rpc.CodeInvalidRequest {
		t.Errorf("err = %v, want denied rpc error", err)
	}
	if len(svc.mgr.List()) != 0 {
		t.Error("a denied open must not spawn a pane")
	}
}

func TestOpenAgentInitiatedUnknownSession(t *testing.T) {
	svc, _, _ := newService(t, &fakeApprover{outcome: approvals.Outcome{Approved: true}})
	if _, err := svc.open(context.Background(), api.PaneOpenParams{SessionID: "nope"}); err == nil {
		t.Error("open with an unknown session should error")
	}
}

func TestListFiltersByWorkspace(t *testing.T) {
	svc, _, _ := newService(t, &fakeApprover{})
	if _, err := svc.open(context.Background(), api.PaneOpenParams{WorkspaceID: "ws1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.open(context.Background(), api.PaneOpenParams{WorkspaceID: "ws2"}); err != nil {
		t.Fatal(err)
	}
	all, _ := svc.list(context.Background(), api.PaneListParams{})
	if len(all.Panes) != 2 {
		t.Fatalf("all = %d, want 2", len(all.Panes))
	}
	ws1, _ := svc.list(context.Background(), api.PaneListParams{WorkspaceID: "ws1"})
	if len(ws1.Panes) != 1 || ws1.Panes[0].WorkspaceID != "ws1" {
		t.Fatalf("ws1 = %+v", ws1.Panes)
	}
}

func TestReadTail(t *testing.T) {
	svc, _, _ := newService(t, &fakeApprover{})
	pane, err := svc.mgr.Spawn(SpawnConfig{Command: shell, Args: []string{"-c", "printf abcdefgh; sleep 30"}})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	waitFor(t, func() bool { return bytes.Contains(pane.Scrollback(), []byte("abcdefgh")) })

	full, _ := svc.read(context.Background(), api.PaneReadParams{PaneID: pane.ID})
	if !bytes.Contains(full.Data, []byte("abcdefgh")) || !full.Running {
		t.Fatalf("full read = %q running=%v", full.Data, full.Running)
	}
	tail, _ := svc.read(context.Background(), api.PaneReadParams{PaneID: pane.ID, Tail: 3})
	if string(tail.Data) != "fgh" {
		t.Errorf("tail = %q, want fgh", tail.Data)
	}
}

func TestReadAndCloseUnknownPane(t *testing.T) {
	svc, _, _ := newService(t, &fakeApprover{})
	if _, err := svc.read(context.Background(), api.PaneReadParams{PaneID: "nope"}); err == nil {
		t.Error("read of unknown pane should error")
	}
	if _, err := svc.close(context.Background(), api.PaneCloseParams{PaneID: "nope"}); err == nil {
		t.Error("close of unknown pane should error")
	}
}

func TestClose(t *testing.T) {
	svc, _, _ := newService(t, &fakeApprover{})
	info, _ := svc.open(context.Background(), api.PaneOpenParams{WorkspaceID: "ws1"})
	if _, err := svc.close(context.Background(), api.PaneCloseParams{PaneID: info.PaneID}); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, ok := svc.mgr.Get(info.PaneID); ok {
		t.Error("pane should be gone after close")
	}
}

func TestRunGatedAndStreamed(t *testing.T) {
	ap := &fakeApprover{outcome: approvals.Outcome{Approved: true}}
	svc, reg, emit := newService(t, ap)
	worktree := t.TempDir()
	reg.Create("s1", "codex", "ws1", api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"}, worktree)

	opened, err := svc.open(context.Background(), api.PaneOpenParams{WorkspaceID: "ws1", Cwd: worktree})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	stream := api.PaneStream(opened.PaneID)

	if _, err := svc.run(context.Background(), api.PaneRunParams{
		PaneID: opened.PaneID, Command: "echo run-marker-42", SessionID: "s1",
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if ap.kind != api.ApprovalPaneRun {
		t.Fatalf("approval kind = %q, want pane_run", ap.kind)
	}
	var detail api.ApprovalDetail
	_ = json.Unmarshal(ap.detail, &detail)
	if detail.PaneRun == nil || detail.PaneRun.Command != "echo run-marker-42" || detail.PaneRun.Cwd != worktree {
		t.Fatalf("pane_run detail = %+v", detail.PaneRun)
	}

	// The command's output is streamed as pane_output events on the pane stream.
	waitFor(t, func() bool {
		for _, ev := range emit.onStream(stream) {
			if ev.Type != "pane_output" {
				continue
			}
			var out api.PaneOutput
			if json.Unmarshal(ev.Payload, &out) == nil && bytes.Contains(out.Data, []byte("run-marker-42")) {
				return true
			}
		}
		return false
	})
}

func TestRunRequiresSession(t *testing.T) {
	svc, _, _ := newService(t, &fakeApprover{outcome: approvals.Outcome{Approved: true}})
	info, _ := svc.open(context.Background(), api.PaneOpenParams{WorkspaceID: "ws1"})
	if _, err := svc.run(context.Background(), api.PaneRunParams{PaneID: info.PaneID, Command: "ls"}); err == nil {
		t.Error("pane.run without a session should error (task-bound)")
	}
}

func TestRunDeniedDoesNotExecute(t *testing.T) {
	ap := &fakeApprover{outcome: approvals.Outcome{Approved: false}}
	svc, reg, _ := newService(t, ap)
	reg.Create("s1", "codex", "ws1", api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"}, t.TempDir())
	info, _ := svc.open(context.Background(), api.PaneOpenParams{WorkspaceID: "ws1"})

	_, err := svc.run(context.Background(), api.PaneRunParams{PaneID: info.PaneID, Command: "ls", SessionID: "s1"})
	var rpcErr *rpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != rpc.CodeInvalidRequest {
		t.Errorf("err = %v, want denied rpc error", err)
	}
}

func TestPaneExitedEvent(t *testing.T) {
	svc, _, emit := newService(t, &fakeApprover{})
	// Spawn a short-lived pane directly and stream it like open would.
	pane, err := svc.mgr.Spawn(SpawnConfig{Command: shell, Args: []string{"-c", "exit 5"}})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	ch, cancel := pane.Subscribe()
	go svc.streamPane(pane, ch, cancel)
	stream := api.PaneStream(pane.ID)

	waitFor(t, func() bool {
		for _, ev := range emit.onStream(stream) {
			if ev.Type == "pane_exited" {
				var pe api.PaneExited
				_ = json.Unmarshal(ev.Payload, &pe)
				return pe.ExitCode == 5
			}
		}
		return false
	})
}
