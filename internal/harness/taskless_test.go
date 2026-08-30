package harness

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dusto/tend/api"
)

// TestTasklessSessionConversesAndMutatesWithApproval drives the task-optional
// path over a real daemon: a session started with a workspace and no task can
// run a prompt turn (conversation), is listed with no task, AND can perform a
// supervised file mutation — approval is the gate, not task association
// (ADR 0006).
func TestTasklessSessionConversesAndMutatesWithApproval(t *testing.T) {
	sock := fakeDaemon(t)
	c := dial(t, sock)
	mustCall(t, c, "daemon.hello", api.HelloParams{}, &api.HelloResult{})
	mustCall(t, c, "client.register", api.ClientRegisterParams{ClientID: "ed", Role: api.RoleEditor, PromptCapable: true}, &api.ClientRegisterResult{})

	// Start a session with no task — just a provider + workspace + worktree.
	root := t.TempDir()
	var started api.AgentStartResult
	mustCall(t, c, "agent.start", api.AgentStartParams{
		ProviderID:   "codex",
		WorkspaceID:  "ws1",
		WorktreeRoot: root,
	}, &started)
	if started.SessionID == "" {
		t.Fatalf("task-less start = %+v", started)
	}
	mustCall(t, c, "events.subscribe", api.EventsSubscribeParams{StreamID: started.StreamID}, &api.EventsSubscribeResult{})
	wsStream := api.WorkspaceStream("ws1")
	mustCall(t, c, "events.subscribe", api.EventsSubscribeParams{StreamID: wsStream}, &api.EventsSubscribeResult{})

	// It is listed with no task.
	var list api.SessionListResult
	mustCall(t, c, "session.list", api.SessionListParams{}, &list)
	if len(list.Sessions) != 1 || list.Sessions[0].Task != nil {
		t.Fatalf("session.list = %+v, want one task-less session", list.Sessions)
	}

	// Run a turn that stays open (session running) until released, so the
	// approval-gated edit happens mid-turn.
	release := filepath.Join(t.TempDir(), "release")
	turnDone := make(chan error, 1)
	go func() {
		var res api.AgentPromptResult
		cx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		turnDone <- c.Call(cx, "agent.prompt", api.AgentPromptParams{SessionID: started.SessionID, Text: "hold:" + release}, &res)
	}()
	if !c.WaitEventCount(started.StreamID, 4, 5*time.Second) {
		t.Fatalf("task-less turn did not stream; got %v", c.EventTypes(started.StreamID))
	}

	// A supervised edit from the task-less session: file.write blocks on the
	// approval gate (it is NOT refused for lack of a task), the editor approves,
	// and it applies through editor.write_buffer.
	uri := fileURI(filepath.Join(root, "note.md"))
	c.SetOpenBuffer(uri, "old\n")
	tick := int64(1)
	writeDone := make(chan api.FileMutationResult, 1)
	writeErr := make(chan error, 1)
	go func() {
		var res api.FileMutationResult
		cx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := c.Call(cx, "file.write", api.FileWriteParams{
			SessionID: started.SessionID, URI: uri, Content: "new\n",
			Base: api.FileBase{ChangedTick: &tick},
		}, &res); err != nil {
			writeErr <- err
			return
		}
		writeDone <- res
	}()

	var requested api.ApprovalRequested
	deadline := time.Now().Add(3 * time.Second)
	for {
		if ev, ok := findEvent(c.Events(wsStream), "approval_requested"); ok {
			if err := json.Unmarshal(ev.Payload, &requested); err != nil {
				t.Fatalf("unmarshal approval_requested: %v", err)
			}
			break
		}
		select {
		case err := <-writeErr:
			t.Fatalf("task-less file.write errored before the gate (task gate not removed?): %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("no approval_requested for a task-less write; got %v", c.EventTypes(wsStream))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if requested.SessionID != started.SessionID {
		t.Fatalf("approval_requested = %+v, want the task-less session", requested)
	}
	mustCall(t, c, "approval.respond", api.ApprovalRespondParams{ApprovalID: requested.ApprovalID, Approved: true}, &api.ApprovalRespondResult{})

	select {
	case err := <-writeErr:
		t.Fatalf("file.write: %v", err)
	case res := <-writeDone:
		if !res.Applied {
			t.Fatalf("task-less write not applied after approval: %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("file.write did not return after approval")
	}
	if got, ok := c.WroteBuffer(uri); !ok || got != "new\n" {
		t.Fatalf("editor.write_buffer got %q (ok=%v), want the edited buffer", got, ok)
	}

	// Release the held turn.
	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatalf("release: %v", err)
	}
	select {
	case <-turnDone:
	case <-time.After(5 * time.Second):
		t.Fatal("held turn did not end after release")
	}
}
