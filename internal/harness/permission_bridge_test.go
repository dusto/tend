package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dusto/tend/api"
)

// TestACPPermissionBridge drives the ACP-native approval path end to end: the
// agent requests permission for one of its OWN tools over
// session/request_permission (as Claude's Write/Edit/Bash do), the daemon raises
// that as an agent_tool approval on the workspace stream, a prompt-capable client
// resolves it, and the decision is mapped back to the ACP option the agent
// expects. approve -> selected:allow; deny -> selected:reject.
func TestACPPermissionBridge(t *testing.T) {
	for _, tc := range []struct {
		name     string
		approved bool
		wantPerm string
	}{
		{"approve", true, "PERM:selected:allow"},
		{"deny", false, "PERM:selected:reject"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := dial(t, fakeDaemon(t))
			mustCall(t, c, "daemon.hello", api.HelloParams{}, &api.HelloResult{})
			mustCall(t, c, "client.register", api.ClientRegisterParams{ClientID: "ed", Role: api.RoleEditor, PromptCapable: true}, &api.ClientRegisterResult{})

			var started api.AgentStartResult
			mustCall(t, c, "agent.start", api.AgentStartParams{ProviderID: "codex", WorkspaceID: "ws1", WorktreeRoot: t.TempDir()}, &started)
			mustCall(t, c, "events.subscribe", api.EventsSubscribeParams{StreamID: started.StreamID}, &api.EventsSubscribeResult{})
			wsStream := api.WorkspaceStream("ws1")
			mustCall(t, c, "events.subscribe", api.EventsSubscribeParams{StreamID: wsStream}, &api.EventsSubscribeResult{})

			// The turn blocks in session/request_permission until we resolve it.
			turnDone := make(chan error, 1)
			go func() {
				var res api.AgentPromptResult
				cx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				turnDone <- c.Call(cx, "agent.prompt", api.AgentPromptParams{SessionID: started.SessionID, Text: "reqperm"}, &res)
			}()

			requested := waitApproval(t, c, wsStream, 5*time.Second)
			if requested.SessionID != started.SessionID {
				t.Fatalf("approval_requested = %+v, want session %s", requested, started.SessionID)
			}
			if requested.Kind != api.ApprovalAgentTool {
				t.Fatalf("approval kind = %q, want %q", requested.Kind, api.ApprovalAgentTool)
			}

			// The durable snapshot carries the tool detail so a client can review it.
			var list api.ApprovalListResult
			mustCall(t, c, "approval.list", api.ApprovalListParams{SessionID: started.SessionID}, &list)
			if len(list.Approvals) != 1 {
				t.Fatalf("approval.list = %+v, want one", list.Approvals)
			}
			var detail api.ApprovalDetail
			if err := json.Unmarshal(list.Approvals[0].Detail, &detail); err != nil {
				t.Fatalf("detail: %v", err)
			}
			if detail.AgentTool == nil || detail.AgentTool.Title != "Write file" || detail.AgentTool.ToolKind != "edit" {
				t.Fatalf("agent_tool detail = %+v", detail.AgentTool)
			}
			if !strings.Contains(string(detail.AgentTool.RawInput), "file_path") {
				t.Errorf("raw input not carried through: %s", detail.AgentTool.RawInput)
			}

			mustCall(t, c, "approval.respond", api.ApprovalRespondParams{ApprovalID: requested.ApprovalID, Approved: tc.approved}, &api.ApprovalRespondResult{})

			if !waitMessageContains(t, c, started.StreamID, tc.wantPerm, 5*time.Second) {
				t.Fatalf("agent did not receive %q; got messages %q", tc.wantPerm, messagesText(c, started.StreamID))
			}
			select {
			case err := <-turnDone:
				if err != nil {
					t.Fatalf("prompt turn: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("turn did not end after the approval was resolved")
			}
		})
	}
}

// TestACPPermissionCancelEvictsApproval verifies the turn-context binding: while
// a native-tool approval is pending, agent.cancel ends the turn and EVICTS the
// approval (approval_resolved, not answered) rather than leaving a stale pending
// that could later resolve selected:allow against a dead turn.
func TestACPPermissionCancelEvictsApproval(t *testing.T) {
	c := dial(t, fakeDaemon(t))
	mustCall(t, c, "daemon.hello", api.HelloParams{}, &api.HelloResult{})
	mustCall(t, c, "client.register", api.ClientRegisterParams{ClientID: "ed", Role: api.RoleEditor, PromptCapable: true}, &api.ClientRegisterResult{})

	var started api.AgentStartResult
	mustCall(t, c, "agent.start", api.AgentStartParams{ProviderID: "codex", WorkspaceID: "ws1", WorktreeRoot: t.TempDir()}, &started)
	mustCall(t, c, "events.subscribe", api.EventsSubscribeParams{StreamID: started.StreamID}, &api.EventsSubscribeResult{})
	wsStream := api.WorkspaceStream("ws1")
	mustCall(t, c, "events.subscribe", api.EventsSubscribeParams{StreamID: wsStream}, &api.EventsSubscribeResult{})

	turnDone := make(chan error, 1)
	go func() {
		var res api.AgentPromptResult
		cx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		turnDone <- c.Call(cx, "agent.prompt", api.AgentPromptParams{SessionID: started.SessionID, Text: "reqperm"}, &res)
	}()

	requested := waitApproval(t, c, wsStream, 5*time.Second)

	// Cancel the turn while the approval is still pending.
	mustCall(t, c, "agent.cancel", api.AgentCancelParams{SessionID: started.SessionID}, &struct{}{})

	// The approval is evicted (resolved, not approved) — its turn is gone.
	resolved := waitResolved(t, c, wsStream, requested.ApprovalID, 5*time.Second)
	if resolved.Approved {
		t.Errorf("evicted approval should not be approved: %+v", resolved)
	}

	// No stale pending remains, and the evicted id can no longer be answered — so a
	// late respond can never send selected:allow to the provider.
	var list api.ApprovalListResult
	mustCall(t, c, "approval.list", api.ApprovalListParams{}, &list)
	if len(list.Approvals) != 0 {
		t.Errorf("approval.list should be empty after eviction, got %+v", list.Approvals)
	}
	if err := c.Call(context.Background(), "approval.respond", api.ApprovalRespondParams{ApprovalID: requested.ApprovalID, Approved: true}, &api.ApprovalRespondResult{}); err == nil {
		t.Error("responding to an evicted approval should fail, not silently allow")
	}

	select {
	case <-turnDone:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled turn did not end")
	}
}

func waitResolved(t *testing.T, c *Client, stream api.StreamID, id api.ApprovalID, d time.Duration) api.ApprovalResolved {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		for _, ev := range c.Events(stream) {
			if ev.Type != "approval_resolved" {
				continue
			}
			var r api.ApprovalResolved
			if json.Unmarshal(ev.Payload, &r) == nil && r.ApprovalID == id {
				return r
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no approval_resolved for %s on %s; got %v", id, stream, c.EventTypes(stream))
	return api.ApprovalResolved{}
}

func waitApproval(t *testing.T, c *Client, stream api.StreamID, d time.Duration) api.ApprovalRequested {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if ev, ok := findEvent(c.Events(stream), "approval_requested"); ok {
			var r api.ApprovalRequested
			if err := json.Unmarshal(ev.Payload, &r); err != nil {
				t.Fatalf("unmarshal approval_requested: %v", err)
			}
			return r
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no approval_requested on %s; got %v", stream, c.EventTypes(stream))
	return api.ApprovalRequested{}
}

func waitMessageContains(t *testing.T, c *Client, stream api.StreamID, want string, d time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if strings.Contains(messagesText(c, stream), want) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func messagesText(c *Client, stream api.StreamID) string {
	var b strings.Builder
	for _, ev := range c.Events(stream) {
		if ev.Type != "agent_message_chunk" {
			continue
		}
		var m api.AgentMessageChunk
		if json.Unmarshal(ev.Payload, &m) == nil {
			b.WriteString(m.Text)
		}
	}
	return b.String()
}
