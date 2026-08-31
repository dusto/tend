package harness

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/events"
)

// TestM0AcceptanceScenario is the milestone-0 exit criteria end to end against a
// real daemon wired to the fake Codex ACP: create a task, start a task-scoped
// agent (which binds this editor), stream a turn, read the editor's buffer and
// LSP diagnostics over the reverse-RPC, land a supervised edit through the
// approval gate (applied via editor.write_buffer), comment progress to the task,
// and reconnect mid-stream replaying the tail with no loss or duplication.
//
// (Persona is a plugin-side selection stored on the session for prompt
// composition; it is not part of the daemon contract, so it is out of scope for
// this daemon-level acceptance — see the persona picker, tend-48d.6.)
func TestM0AcceptanceScenario(t *testing.T) {
	sock := fakeDaemon(t)
	c := dial(t, sock)
	mustCall(t, c, "daemon.hello", api.HelloParams{}, &api.HelloResult{})
	mustCall(t, c, "client.register", api.ClientRegisterParams{ClientID: "ed", Role: api.RoleEditor, PromptCapable: true}, &api.ClientRegisterResult{})

	// Create a task from the editor.
	var task api.Task
	mustCall(t, c, "task.create", api.TaskCreateParams{WorkspaceID: "ws1", Title: "fix the bug"}, &task)
	if task.Ref.ID == "" {
		t.Fatalf("task.create = %+v", task)
	}

	// Start a task-scoped agent (binds this editor).
	root := t.TempDir()
	var started api.AgentStartResult
	mustCall(t, c, "agent.start", api.AgentStartParams{
		ProviderID:   "codex",
		Task:         task.Ref,
		WorktreeRoot: root,
	}, &started)
	mustCall(t, c, "events.subscribe", api.EventsSubscribeParams{StreamID: started.StreamID}, &api.EventsSubscribeResult{})
	// Approvals broadcast on the repo-wide workspace stream, not the session
	// stream: subscribe there to receive the gate's approval_requested live.
	wsStream := api.WorkspaceStream("ws1")
	mustCall(t, c, "events.subscribe", api.EventsSubscribeParams{StreamID: wsStream}, &api.EventsSubscribeResult{})

	// Run a turn that stays open (session running) until we release it, so the
	// approval-gated edit happens mid-turn — the only state an approval is legal.
	release := filepath.Join(t.TempDir(), "release")
	turnDone := make(chan error, 1)
	go func() {
		var res api.AgentPromptResult
		cx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		turnDone <- c.Call(cx, "agent.prompt", api.AgentPromptParams{SessionID: started.SessionID, Text: "hold:" + release}, &res)
	}()
	// The four streamed updates mean the turn is mid-flight (running); turn_end
	// is not published until the turn returns after release.
	if !c.WaitEventCount(started.StreamID, 4, 5*time.Second) {
		t.Fatalf("turn did not stream; got %v", c.EventTypes(started.StreamID))
	}

	// The agent inspects the editor's live buffer and LSP diagnostics over the
	// daemon->editor reverse-RPC (routed because agent.start bound this editor).
	uri := fileURI(filepath.Join(root, "main.go"))
	c.SetOpenBuffer(uri, "package main\n")
	c.SetDiagnostics(uri, []api.Diagnostic{{Severity: api.SeverityWarning, Message: "unused"}})

	var rd api.FileReadResult
	mustCall(t, c, "file.read", api.FileReadParams{SessionID: started.SessionID, URI: uri}, &rd)
	if !rd.Open || rd.Content != "package main\n" {
		t.Fatalf("file.read = %+v, want the live buffer", rd)
	}
	var ld api.LSPDiagnosticsResult
	mustCall(t, c, "lsp.diagnostics", api.LSPDiagnosticsParams{SessionID: started.SessionID, URI: uri}, &ld)
	if len(ld.Diagnostics) != 1 || ld.Diagnostics[0].Message != "unused" {
		t.Fatalf("lsp.diagnostics = %+v, want the editor's diagnostics", ld)
	}

	// A supervised edit: file.patch blocks on the approval gate, the editor
	// approves the raised prompt, and the daemon applies it through
	// editor.write_buffer (the open buffer, not disk).
	tick := int64(1)
	patchDone := make(chan api.FileMutationResult, 1)
	patchErr := make(chan error, 1)
	go func() {
		var res api.FileMutationResult
		cx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := c.Call(cx, "file.patch", api.FilePatchParams{
			SessionID: started.SessionID,
			URI:       uri,
			Edits: []api.TextEdit{{
				Range:   api.Range{Start: api.Position{Line: 0, ByteCol: 12}, End: api.Position{Line: 0, ByteCol: 12}},
				NewText: " // edited",
			}},
			Base: api.FileBase{ChangedTick: &tick},
		}, &res)
		if err != nil {
			patchErr <- err
			return
		}
		patchDone <- res
	}()

	// The workspace stream also carries repo-wide events (e.g. task_created), so
	// wait for and pick out the approval_requested broadcast specifically.
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
		case err := <-patchErr:
			t.Fatalf("file.patch errored before the gate: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("no approval_requested broadcast on the workspace stream; got %v", c.EventTypes(wsStream))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if requested.ApprovalID == "" || requested.SessionID != started.SessionID {
		t.Fatalf("approval_requested = %+v, want a session approval", requested)
	}
	mustCall(t, c, "approval.respond", api.ApprovalRespondParams{ApprovalID: requested.ApprovalID, Approved: true}, &api.ApprovalRespondResult{})

	select {
	case err := <-patchErr:
		t.Fatalf("file.patch: %v", err)
	case res := <-patchDone:
		if !res.Applied {
			t.Fatalf("edit not applied: %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("file.patch did not return after approval")
	}
	// Applied through the editor reverse call, to the buffer — not disk.
	if got, ok := c.WroteBuffer(uri); !ok || got != "package main // edited\n" {
		t.Fatalf("editor.write_buffer got %q (ok=%v), want the edited buffer", got, ok)
	}

	// Comment progress back to the task; the comment is persisted on the task.
	var commented api.Task
	mustCall(t, c, "task.comment", api.TaskCommentParams{Ref: task.Ref, Text: "applied the fix", Author: "agent"}, &commented)
	var shown api.Task
	mustCall(t, c, "task.show", api.TaskShowParams{Ref: task.Ref}, &shown)
	if len(shown.Comments) != 1 || shown.Comments[0].Text != "applied the fix" {
		t.Fatalf("task comments = %+v, want the progress comment", shown.Comments)
	}

	// Reconnect mid-turn — the turn is still held (running), so the session
	// stream carries seqs 1..7 (user_prompt + agent_prompt_usage + four turn
	// updates + the artifact_written record from the applied edit) but not yet
	// turn_end. The approval pair is NOT here: it broadcast on the workspace
	// stream. A fresh connection resumes from a cursor behind the tail and replays
	// (2, tail] = seqs 3..7; a Deduper seeded with the first delivery drops every
	// redelivered record.
	//
	// Wait for the held-turn tail (1..7) to be fully delivered to the original
	// connection first, so the Deduper seed and the replay extent are
	// deterministic.
	if !c.WaitEventCount(started.StreamID, 7, 3*time.Second) {
		t.Fatalf("held-turn events not fully delivered; got %v", c.EventTypes(started.StreamID))
	}
	dd := events.NewDeduper()
	for _, ev := range c.Events(started.StreamID) {
		dd.Fresh(ev)
	}
	rc := dial(t, sock)
	mustCall(t, rc, "events.subscribe", api.EventsSubscribeParams{StreamID: started.StreamID, LastSeq: 2}, &api.EventsSubscribeResult{})
	if !rc.WaitEventCount(started.StreamID, 5, 3*time.Second) {
		t.Fatalf("mid-turn replay incomplete; got %v", rc.EventTypes(started.StreamID))
	}
	for _, ev := range rc.Events(started.StreamID) {
		if ev.Seq < 3 || ev.Seq > 7 {
			t.Errorf("replay redelivered seq %d, want only (2, tail] = 3..7", ev.Seq)
		}
		if dd.Fresh(ev) {
			t.Errorf("redelivered seq %d should dedup as already-seen", ev.Seq)
		}
	}

	// Now release the held turn: turn_end (seq 8) is published while the
	// reconnected client is live-subscribed, so it must arrive on rc exactly
	// once as a fresh record — the "no missing or duplicated events across a
	// mid-turn reconnect" guarantee.
	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := <-turnDone; err != nil {
		t.Fatalf("agent.prompt: %v", err)
	}
	if !rc.WaitEventCount(started.StreamID, 6, 3*time.Second) {
		t.Fatalf("reconnected client did not receive the live turn_end; got %v", rc.EventTypes(started.StreamID))
	}
	var turnEnds int
	for _, ev := range rc.Events(started.StreamID) {
		if ev.Type == "turn_end" {
			turnEnds++
			if ev.Seq != 8 {
				t.Errorf("turn_end seq = %d, want 8", ev.Seq)
			}
			if !dd.Fresh(ev) {
				t.Errorf("live turn_end should be fresh on the reconnected client, not a duplicate")
			}
		}
	}
	if turnEnds != 1 {
		t.Errorf("reconnected client received turn_end %d times, want exactly once", turnEnds)
	}
	// The original connection sees the completed turn too, ending in turn_end.
	if !c.WaitEventCount(started.StreamID, 8, 3*time.Second) {
		t.Fatalf("original client turn did not complete; got %v", c.EventTypes(started.StreamID))
	}
	if types := c.EventTypes(started.StreamID); types[len(types)-1] != "turn_end" {
		t.Errorf("original stream did not end in turn_end; got %v", types)
	}
}

// findEvent returns the first event of the given type in evs, if any.
func findEvent(evs []api.Event, typ string) (api.Event, bool) {
	for _, ev := range evs {
		if ev.Type == typ {
			return ev, true
		}
	}
	return api.Event{}, false
}
