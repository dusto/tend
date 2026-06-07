package harness

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/daemon"
	"github.com/dusto/tend/internal/events"
	"github.com/dusto/tend/internal/rpc"
)

// startTurn registers an editor client, starts a codex session against the fake
// ACP, subscribes its stream, and runs one prompt, returning the client and the
// session's stream id once the turn's events have arrived.
func startTurn(t *testing.T, sock string) (*Client, api.StreamID) {
	t.Helper()
	c := dial(t, sock)
	mustCall(t, c, "daemon.hello", api.HelloParams{}, &api.HelloResult{})
	mustCall(t, c, "client.register", api.ClientRegisterParams{ClientID: "ed", Role: api.RoleEditor, PromptCapable: true}, &api.ClientRegisterResult{})

	var started api.AgentStartResult
	mustCall(t, c, "agent.start", api.AgentStartParams{
		ProviderID:   "codex",
		Task:         api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"},
		WorktreeRoot: t.TempDir(),
	}, &started)
	mustCall(t, c, "events.subscribe", api.EventsSubscribeParams{StreamID: started.StreamID}, &api.EventsSubscribeResult{})
	mustCall(t, c, "agent.prompt", api.AgentPromptParams{SessionID: started.SessionID, Text: "hi"}, &api.AgentPromptResult{})

	if !c.WaitEventCount(started.StreamID, 5, 3*time.Second) {
		t.Fatalf("turn events did not arrive; got %v", c.EventTypes(started.StreamID))
	}
	return c, started.StreamID
}

// TestReplayReconnectDedup proves at-least-once replay + dedup over the socket: a
// reconnect from an older cursor replays the tail, and the client's Deduper drops
// the redelivered records by (stream_id, seq, kind).
func TestReplayReconnectDedup(t *testing.T) {
	sock := fakeDaemon(t)
	first, stream := startTurn(t, sock)

	// The first connection processed seqs 1..5.
	dd := events.NewDeduper()
	got := first.Events(stream)
	if len(got) != 5 {
		t.Fatalf("first delivery = %d events, want 5", len(got))
	}
	for _, ev := range got {
		if !dd.Fresh(ev) {
			t.Errorf("first delivery seq %d should be fresh", ev.Seq)
		}
	}

	// Reconnect with a cursor behind what was processed (last persisted = 2): the
	// daemon replays (2, tail] = 3,4,5, which the Deduper drops as duplicates.
	reconnect := dial(t, sock)
	mustCall(t, reconnect, "events.subscribe", api.EventsSubscribeParams{StreamID: stream, LastSeq: 2}, &api.EventsSubscribeResult{})
	if !reconnect.WaitEventCount(stream, 3, 3*time.Second) {
		t.Fatalf("replay did not redeliver the tail; got %v", reconnect.EventTypes(stream))
	}
	replayed := reconnect.Events(stream)
	for _, ev := range replayed {
		if ev.Seq < 3 {
			t.Errorf("replay redelivered seq %d, want only (2, tail]", ev.Seq)
		}
		if dd.Fresh(ev) {
			t.Errorf("redelivered seq %d should dedup as already-seen", ev.Seq)
		}
	}
}

// TestPerStreamMultiplexing proves logical streams are independent over one
// connection: the session turn and a workspace task event land on their own
// streams with no cross-contamination.
func TestPerStreamMultiplexing(t *testing.T) {
	sock := fakeDaemon(t)
	c, sessionStream := startTurn(t, sock)
	workspaceStream := api.WorkspaceStream("ws1")

	mustCall(t, c, "events.subscribe", api.EventsSubscribeParams{StreamID: workspaceStream}, &api.EventsSubscribeResult{})
	mustCall(t, c, "task.create", api.TaskCreateParams{WorkspaceID: "ws1", Title: "do it"}, &api.Task{})

	if !c.WaitEventCount(workspaceStream, 1, 3*time.Second) {
		t.Fatalf("workspace stream got no task event; got %v", c.EventTypes(workspaceStream))
	}
	if ws := c.EventTypes(workspaceStream); len(ws) != 1 || ws[0] != "task_created" {
		t.Errorf("workspace stream = %v, want [task_created]", ws)
	}
	// The session stream still carries only the turn, not the task event.
	for _, typ := range c.EventTypes(sessionStream) {
		if typ == "task_created" {
			t.Error("task event leaked onto the session stream")
		}
	}
}

// TestCursorCompactedAndSummaryAdvance proves compaction replay over the socket:
// a cursor before a compacted range receives the summary (advancing across the
// range) then the tail; a cursor inside the range is rejected with
// cursor_compacted.
func TestCursorCompactedAndSummaryAdvance(t *testing.T) {
	// Retention 0 so a recent range can be compacted in the test.
	srv, sock, err := StartServer(t.TempDir(), daemon.WithEventRetention(0))
	if err != nil {
		t.Fatalf("StartServer: %v", err)
	}
	t.Cleanup(srv.Shutdown)

	store := srv.EventStore()
	stream := api.StreamID("session:comp")
	for range 5 {
		if _, err := store.Publish(api.Event{StreamID: stream, Scope: api.ScopeSession, Type: "tool_call"}); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}
	if err := store.Compact(stream, api.ScopeSession, 2, 4, nil); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// A cursor before the range: raw 1, the summary (served at 2, advancing the
	// cursor to 4), then raw 5.
	c := dial(t, sock)
	mustCall(t, c, "events.subscribe", api.EventsSubscribeParams{StreamID: stream, LastSeq: 0}, &api.EventsSubscribeResult{})
	if !c.WaitEventCount(stream, 3, 3*time.Second) {
		t.Fatalf("compacted replay incomplete; got %v", c.EventTypes(stream))
	}
	got := c.Events(stream)
	if got[0].Seq != 1 || got[0].Kind != api.KindEvent {
		t.Errorf("record 0 = %+v, want raw seq 1", got[0])
	}
	if got[1].Kind != api.KindSummary || got[1].Seq != 2 || got[1].CursorSeq != 4 {
		t.Errorf("record 1 = %+v, want summary seq 2 cursor 4", got[1])
	}
	if got[2].Seq != 5 || got[2].Kind != api.KindEvent {
		t.Errorf("record 2 = %+v, want raw seq 5", got[2])
	}

	// A cursor inside [2,4): cursor_compacted at the from boundary.
	inside := dial(t, sock)
	cx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err = inside.Call(cx, "events.subscribe", api.EventsSubscribeParams{StreamID: stream, LastSeq: 3}, &api.EventsSubscribeResult{})
	var rpcErr *rpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != api.ErrCursorCompacted {
		t.Fatalf("err = %v, want cursor_compacted", err)
	}
}
